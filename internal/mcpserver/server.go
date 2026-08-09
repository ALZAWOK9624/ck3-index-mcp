package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"ck3-index/internal/buildinfo"
	"ck3-index/internal/indexer"
)

// Serve runs the MCP server over the provided streams. Source files remain
// read-only to the server; ck3_refresh updates only the rebuildable cache via
// its own transactional indexer connection.
func Serve(ctx context.Context, cfg indexer.Config, dbPath string, in io.Reader, out io.Writer) error {
	return serveWithToolCaller(ctx, cfg, dbPath, in, out, callMCPTool)
}

// mcpToolCaller is an internal seam for lifecycle tests. It keeps the public
// Serve entry point stable while allowing cancellation delivery to be tested
// without making a real CK3 operation artificially slow.
type mcpToolCaller func(context.Context, *indexer.DB, indexer.Config, json.RawMessage) (any, error)

type mcpReadEvent struct {
	Request rpcRequest
	Err     error
}

type mcpToolTask struct {
	cancelled bool
	class     mcpTaskClass
	cancel    context.CancelFunc
	phase     mcpTaskPhase
	params    json.RawMessage
	requestID json.RawMessage
	idKey     string
	queuedAt  time.Time
	startedAt time.Time
}

type mcpToolTaskResult struct {
	idKey     string
	response  rpcResponse
	committed bool
}

type mcpCommitState struct{ committed bool }
type mcpCommitStateKey struct{}

func markMCPCommitted(ctx context.Context) {
	if state, ok := ctx.Value(mcpCommitStateKey{}).(*mcpCommitState); ok {
		state.committed = true
	}
}

type mcpSession struct {
	initialized        bool
	clientInitialized  bool
	clientName         string
	clientVersion      string
	clientCapabilities map[string]json.RawMessage
	seenRequestIDs     map[string]struct{}
}

func (s mcpSession) readyForTools() bool {
	return s.initialized && s.clientInitialized
}

type mcpTaskClass string

type mcpTaskPhase string

const (
	mcpTaskRead   mcpTaskClass = "read"
	mcpTaskHeavy  mcpTaskClass = "heavy"
	mcpTaskRaster mcpTaskClass = "raster"

	mcpTaskQueued  mcpTaskPhase = "queue"
	mcpTaskRunning mcpTaskPhase = "execution"

	maxMCPTasks       = indexer.DefaultMCPMaxTasks
	maxMCPHeavyTasks  = indexer.DefaultMCPMaxHeavyTasks
	maxMCPRasterTasks = indexer.DefaultMCPMaxRasterTasks
	maxMCPQueuedTasks = indexer.DefaultMCPMaxQueuedTasks
)

type mcpTaskLimiter struct {
	active       int
	heavy        int
	raster       int
	queued       int
	queuedHeavy  int
	queuedRaster int
	maxActive    int
	maxHeavy     int
	maxRaster    int
	maxQueued    int
	trackProcess bool
}

var processMCPTaskUsage struct {
	active       atomic.Int64
	heavy        atomic.Int64
	raster       atomic.Int64
	queued       atomic.Int64
	queuedHeavy  atomic.Int64
	queuedRaster atomic.Int64
}

type mcpTaskUsageSnapshot struct {
	Active            int
	Heavy             int
	Raster            int
	Queued            int
	QueuedHeavy       int
	QueuedRaster      int
	EstimatedMemoryMB int
}

const (
	estimatedReadTaskMemoryMB   = 8
	estimatedHeavyTaskMemoryMB  = 192
	estimatedRasterTaskMemoryMB = 768
)

func currentMCPTaskUsage() mcpTaskUsageSnapshot {
	active := int(processMCPTaskUsage.active.Load())
	heavy := int(processMCPTaskUsage.heavy.Load())
	raster := int(processMCPTaskUsage.raster.Load())
	queued := int(processMCPTaskUsage.queued.Load())
	queuedHeavy := int(processMCPTaskUsage.queuedHeavy.Load())
	queuedRaster := int(processMCPTaskUsage.queuedRaster.Load())
	reads := active - heavy - raster
	if reads < 0 {
		reads = 0
	}
	return mcpTaskUsageSnapshot{
		Active: active, Heavy: heavy, Raster: raster,
		Queued: queued, QueuedHeavy: queuedHeavy, QueuedRaster: queuedRaster,
		EstimatedMemoryMB: reads*estimatedReadTaskMemoryMB + heavy*estimatedHeavyTaskMemoryMB + raster*estimatedRasterTaskMemoryMB,
	}
}

func isExpensiveMCPTask(class mcpTaskClass) bool {
	return class == mcpTaskHeavy || class == mcpTaskRaster
}

func (limiter *mcpTaskLimiter) acquire(class mcpTaskClass) bool {
	maxActive, maxHeavy, maxRaster := limiter.limits()
	if limiter.active >= maxActive {
		return false
	}
	if isExpensiveMCPTask(class) && limiter.heavy+limiter.raster >= maxHeavy {
		return false
	}
	if class == mcpTaskRaster && limiter.raster >= maxRaster {
		return false
	}
	limiter.active++
	if class == mcpTaskHeavy {
		limiter.heavy++
	}
	if class == mcpTaskRaster {
		limiter.raster++
	}
	if limiter.trackProcess {
		processMCPTaskUsage.active.Add(1)
		if class == mcpTaskHeavy {
			processMCPTaskUsage.heavy.Add(1)
		}
		if class == mcpTaskRaster {
			processMCPTaskUsage.raster.Add(1)
		}
	}
	return true
}

func (limiter *mcpTaskLimiter) limits() (int, int, int) {
	active, heavy, raster := limiter.maxActive, limiter.maxHeavy, limiter.maxRaster
	if active <= 0 {
		active = maxMCPTasks
	}
	if heavy <= 0 {
		heavy = maxMCPHeavyTasks
	}
	if raster <= 0 {
		raster = maxMCPRasterTasks
	}
	return active, heavy, raster
}

func (limiter *mcpTaskLimiter) queueLimit() int {
	if limiter.maxQueued > 0 {
		return limiter.maxQueued
	}
	return maxMCPQueuedTasks
}

func (limiter *mcpTaskLimiter) diagnostics() map[string]any {
	maxActive, maxHeavy, maxRaster := limiter.limits()
	return map[string]any{
		"active_tasks":        limiter.active,
		"active_heavy_tasks":  limiter.heavy,
		"active_raster_tasks": limiter.raster,
		"queued_tasks":        limiter.queued,
		"queued_heavy_tasks":  limiter.queuedHeavy,
		"queued_raster_tasks": limiter.queuedRaster,
		"max_tasks":           maxActive,
		"max_expensive_tasks": maxHeavy,
		"max_raster_tasks":    maxRaster,
		"max_queued_tasks":    limiter.queueLimit(),
	}
}

func newMCPTaskLimiter(cfg indexer.Config) mcpTaskLimiter {
	active, heavy, raster, queued := cfg.MCPMaxTasks, cfg.MCPMaxHeavyTasks, cfg.MCPMaxRasterTasks, cfg.MCPMaxQueuedTasks
	if active <= 0 {
		active = maxMCPTasks
	}
	if heavy <= 0 {
		heavy = maxMCPHeavyTasks
	}
	if raster <= 0 {
		raster = maxMCPRasterTasks
	}
	if queued <= 0 {
		queued = maxMCPQueuedTasks
	}
	return mcpTaskLimiter{maxActive: active, maxHeavy: heavy, maxRaster: raster, maxQueued: queued, trackProcess: true}
}

func (limiter *mcpTaskLimiter) enqueue(class mcpTaskClass) bool {
	if limiter.queued >= limiter.queueLimit() {
		return false
	}
	limiter.queued++
	if class == mcpTaskHeavy {
		limiter.queuedHeavy++
	}
	if class == mcpTaskRaster {
		limiter.queuedRaster++
	}
	if limiter.trackProcess {
		processMCPTaskUsage.queued.Add(1)
		if class == mcpTaskHeavy {
			processMCPTaskUsage.queuedHeavy.Add(1)
		}
		if class == mcpTaskRaster {
			processMCPTaskUsage.queuedRaster.Add(1)
		}
	}
	return true
}

func (limiter *mcpTaskLimiter) dequeue(class mcpTaskClass) {
	if limiter.queued <= 0 {
		return
	}
	limiter.queued--
	if class == mcpTaskHeavy && limiter.queuedHeavy > 0 {
		limiter.queuedHeavy--
	}
	if class == mcpTaskRaster && limiter.queuedRaster > 0 {
		limiter.queuedRaster--
	}
	if limiter.trackProcess {
		processMCPTaskUsage.queued.Add(-1)
		if class == mcpTaskHeavy {
			processMCPTaskUsage.queuedHeavy.Add(-1)
		}
		if class == mcpTaskRaster {
			processMCPTaskUsage.queuedRaster.Add(-1)
		}
	}
}

func (limiter *mcpTaskLimiter) release(class mcpTaskClass) {
	if limiter.active <= 0 {
		return
	}
	limiter.active--
	if class == mcpTaskHeavy && limiter.heavy > 0 {
		limiter.heavy--
	}
	if class == mcpTaskRaster && limiter.raster > 0 {
		limiter.raster--
	}
	if limiter.trackProcess {
		processMCPTaskUsage.active.Add(-1)
		if class == mcpTaskHeavy {
			processMCPTaskUsage.heavy.Add(-1)
		}
		if class == mcpTaskRaster {
			processMCPTaskUsage.raster.Add(-1)
		}
	}
}

func (limiter *mcpTaskLimiter) close() {
	if !limiter.trackProcess {
		return
	}
	processMCPTaskUsage.active.Add(-int64(limiter.active))
	processMCPTaskUsage.heavy.Add(-int64(limiter.heavy))
	processMCPTaskUsage.raster.Add(-int64(limiter.raster))
	processMCPTaskUsage.queued.Add(-int64(limiter.queued))
	processMCPTaskUsage.queuedHeavy.Add(-int64(limiter.queuedHeavy))
	processMCPTaskUsage.queuedRaster.Add(-int64(limiter.queuedRaster))
	limiter.active = 0
	limiter.heavy = 0
	limiter.raster = 0
	limiter.queued = 0
	limiter.queuedHeavy = 0
	limiter.queuedRaster = 0
}

func classifyMCPTask(raw json.RawMessage) mcpTaskClass {
	var call callToolParams
	if err := json.Unmarshal(raw, &call); err != nil {
		return mcpTaskRead
	}
	switch strings.ToLower(strings.TrimSpace(call.Name)) {
	case "map_render", "map_terrain_edit", "map_split_province", "map_apply_split":
		return mcpTaskRaster
	case "ck3_save":
		// card and compatibility read only the header and metadata; audit
		// and character stream the whole gamestate.
		return saveTaskClass(call.Arguments)
	case "ck3_review", "ck3_preflight", "ck3_impact", "ck3_refresh", "ck3_package", "ck3_gui",
		"map_asset_audit", "map_province_mapping", "map_build_metric", "map_physical_context", "map_route",
		"map_migration_snapshot", "map_province_migration", "map_assignment_plan", "map_building_candidates":
		return mcpTaskHeavy
	case "ck3_workspace":
		return workspaceTaskClass(call.Arguments)
	case "ck3_dependencies":
		return dependencyTaskClass(call.Arguments)
	default:
		return mcpTaskRead
	}
}

func workspaceTaskClass(arguments json.RawMessage) mcpTaskClass {
	var args struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return mcpTaskHeavy
	}
	if strings.EqualFold(strings.TrimSpace(args.Operation), "capabilities") {
		return mcpTaskRead
	}
	return mcpTaskHeavy
}

func dependencyTaskClass(arguments json.RawMessage) mcpTaskClass {
	var args struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return mcpTaskHeavy
	}
	if strings.EqualFold(strings.TrimSpace(args.Operation), "event_chain") {
		return mcpTaskHeavy
	}
	return mcpTaskRead
}

func mcpQueueTimeout(cfg indexer.Config) time.Duration {
	seconds := cfg.MCPQueueTimeoutSeconds
	if seconds <= 0 {
		seconds = indexer.DefaultMCPQueueTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func mcpExecutionTimeout(cfg indexer.Config) time.Duration {
	seconds := cfg.MCPExecutionTimeoutSeconds
	if seconds <= 0 {
		seconds = indexer.DefaultMCPExecutionTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func serveWithToolCaller(ctx context.Context, cfg indexer.Config, dbPath string, in io.Reader, out io.Writer, caller mcpToolCaller) error {
	db, err := openMCPDatabase(ctx, cfg, dbPath)
	if err != nil {
		return err
	}
	if err := db.RestoreEngineRules(ctx, cfg.EngineLogs); err != nil {
		_ = db.Close()
		return err
	}
	databaseManager, err := newMCPDatabaseManager(cfg, dbPath, db)
	if err != nil {
		_ = db.Close()
		return err
	}
	defer databaseManager.Close()

	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	readEvents := startMCPReader(sessionCtx, bufio.NewReaderSize(in, 4*1024*1024))
	configuredTasks := cfg.MCPMaxTasks
	if configuredTasks <= 0 {
		configuredTasks = maxMCPTasks
	}
	taskResults := make(chan mcpToolTaskResult, configuredTasks)
	tasks := map[string]mcpToolTask{}
	queuedTaskIDs := make([]string, 0, configuredTasks)
	inputClosed := false
	session := mcpSession{seenRequestIDs: map[string]struct{}{}}
	limiter := newMCPTaskLimiter(cfg)
	defer limiter.close()
	queueTimeout := mcpQueueTimeout(cfg)
	executionTimeout := mcpExecutionTimeout(cfg)
	var queueTimer *time.Timer
	var queueTimerC <-chan time.Time
	defer func() {
		if queueTimer != nil {
			queueTimer.Stop()
		}
	}()

	writeResponse := func(response rpcResponse) error {
		return writeMCPMessage(out, response)
	}
	startTask := func(task mcpToolTask) {
		task.phase = mcpTaskRunning
		task.startedAt = time.Now()
		taskCtx, taskCancel := context.WithTimeout(sessionCtx, executionTimeout)
		task.cancel = taskCancel
		tasks[task.idKey] = task
		lease, leaseErr := databaseManager.Acquire()
		if leaseErr != nil {
			taskResults <- mcpToolTaskResult{
				idKey: task.idKey,
				response: rpcResponse{JSONRPC: "2.0", ID: task.requestID, Result: encodeToolError(newToolError(
					ErrorDatabaseSwitchUnavailable, "database", "the MCP database manager is not available", true, nil, nil,
				), nil)},
			}
			return
		}
		taskCtx = withMCPDatabaseContext(taskCtx, databaseManager, lease.Identity)
		go runMCPToolTask(taskCtx, sessionCtx, caller, lease.DB, lease.Config, task.params, task.requestID, task.idKey, task.class, task.queuedAt, task.startedAt, executionTimeout, lease.Release, taskResults)
	}
	compactQueue := func() {
		kept := queuedTaskIDs[:0]
		for _, id := range queuedTaskIDs {
			task, ok := tasks[id]
			if ok && task.phase == mcpTaskQueued {
				kept = append(kept, id)
			}
		}
		queuedTaskIDs = kept
	}
	dispatchQueued := func() {
		for {
			compactQueue()
			selected := -1
			var task mcpToolTask
			for index, id := range queuedTaskIDs {
				candidate := tasks[id]
				if limiter.acquire(candidate.class) {
					selected = index
					task = candidate
					break
				}
			}
			if selected < 0 {
				return
			}
			queuedTaskIDs = append(queuedTaskIDs[:selected], queuedTaskIDs[selected+1:]...)
			limiter.dequeue(task.class)
			startTask(task)
		}
	}
	queueTimeoutResponse := func(task mcpToolTask, now time.Time) rpcResponse {
		details := limiter.diagnostics()
		details["phase"] = string(mcpTaskQueued)
		details["task_class"] = string(task.class)
		details["queue_ms"] = now.Sub(task.queuedAt).Milliseconds()
		details["queue_timeout_ms"] = queueTimeout.Milliseconds()
		return rpcResponse{JSONRPC: "2.0", ID: task.requestID, Result: encodeToolError(newToolError(
			ErrorQueueTimeout,
			"concurrency",
			"the operation exceeded the MCP queue time limit before execution began",
			true,
			details,
			map[string]any{"guidance": "Retry after active expensive operations finish, or narrow the request."},
		), nil)}
	}
	expireQueued := func(now time.Time) error {
		kept := queuedTaskIDs[:0]
		for _, id := range queuedTaskIDs {
			task, ok := tasks[id]
			if !ok || task.phase != mcpTaskQueued {
				continue
			}
			if now.Before(task.queuedAt.Add(queueTimeout)) {
				kept = append(kept, id)
				continue
			}
			delete(tasks, id)
			limiter.dequeue(task.class)
			if !task.cancelled {
				if err := writeResponse(queueTimeoutResponse(task, now)); err != nil {
					queuedTaskIDs = kept
					return err
				}
			}
		}
		queuedTaskIDs = kept
		return nil
	}
	resetQueueTimer := func() {
		var earliest time.Time
		for _, id := range queuedTaskIDs {
			task, ok := tasks[id]
			if !ok || task.phase != mcpTaskQueued {
				continue
			}
			deadline := task.queuedAt.Add(queueTimeout)
			if earliest.IsZero() || deadline.Before(earliest) {
				earliest = deadline
			}
		}
		if earliest.IsZero() {
			if queueTimer != nil && !queueTimer.Stop() {
				select {
				case <-queueTimer.C:
				default:
				}
			}
			queueTimerC = nil
			return
		}
		wait := time.Until(earliest)
		if wait < 0 {
			wait = 0
		}
		if queueTimer == nil {
			queueTimer = time.NewTimer(wait)
		} else {
			if !queueTimer.Stop() {
				select {
				case <-queueTimer.C:
				default:
				}
			}
			queueTimer.Reset(wait)
		}
		queueTimerC = queueTimer.C
	}
	cancelTask := func(id string) {
		task, active := tasks[id]
		if !active {
			return
		}
		task.cancelled = true
		if task.phase == mcpTaskQueued {
			delete(tasks, id)
			limiter.dequeue(task.class)
			return
		}
		if task.cancel != nil {
			task.cancel()
		}
		tasks[id] = task
	}
	cancelTasks := func() {
		for id, task := range tasks {
			task.cancelled = true
			if task.phase == mcpTaskQueued {
				limiter.dequeue(task.class)
				delete(tasks, id)
				continue
			}
			if task.cancel != nil {
				task.cancel()
			}
			tasks[id] = task
		}
		queuedTaskIDs = nil
	}

	for !inputClosed || len(tasks) > 0 {
		resetQueueTimer()
		select {
		case <-ctx.Done():
			cancelTasks()
			return ctx.Err()
		case <-queueTimerC:
			if err := expireQueued(time.Now()); err != nil {
				cancelTasks()
				return err
			}
		case event, ok := <-readEvents:
			if !ok {
				inputClosed = true
				readEvents = nil
				cancelTasks()
				break
			}
			if event.Err != nil {
				var rpcErr *protocolError
				if !errors.As(event.Err, &rpcErr) {
					cancelTasks()
					return event.Err
				}
				if err := writeResponse(rpcResponse{JSONRPC: "2.0", ID: jsonNullID(), Error: rpcErr}); err != nil {
					cancelTasks()
					return err
				}
				if rpcErr.Fatal {
					cancelTasks()
					return nil
				}
				break
			}
			req := event.Request
			if req.hasID && !validRPCRequestID(req.ID) {
				if err := writeResponse(rpcResponse{JSONRPC: "2.0", ID: jsonNullID(), Error: newProtocolError(rpcInvalidRequest, "request id must be a string or integer")}); err != nil {
					cancelTasks()
					return err
				}
				break
			}
			if !req.hasID {
				if id, cancelled := handleMCPNotification(req, &session); cancelled {
					cancelTask(id)
				}
				break
			}
			idKey, _ := normalizedRPCRequestID(req.ID)
			if _, duplicate := session.seenRequestIDs[idKey]; duplicate {
				if err := writeResponse(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: newProtocolError(rpcInvalidRequest, "request id has already been used in this MCP session")}); err != nil {
					cancelTasks()
					return err
				}
				break
			}
			session.seenRequestIDs[idKey] = struct{}{}

			response := rpcResponse{JSONRPC: "2.0", ID: req.ID}
			if req.JSONRPC != "2.0" || req.Method == "" {
				response.Error = newProtocolError(rpcInvalidRequest, "request must use jsonrpc=2.0 and include method")
				if err := writeResponse(response); err != nil {
					cancelTasks()
					return err
				}
				break
			}
			switch req.Method {
			case "initialize":
				if session.initialized {
					response.Error = newProtocolError(rpcInvalidRequest, "initialize has already completed for this MCP session")
					if err := writeResponse(response); err != nil {
						cancelTasks()
						return err
					}
					break
				}
				initialize, initErr := parseMCPInitializeParams(req.Params)
				if initErr != nil {
					response.Error = initErr
					if err := writeResponse(response); err != nil {
						cancelTasks()
						return err
					}
					break
				}
				session.initialized = true
				session.clientName = initialize.ClientName
				session.clientVersion = initialize.ClientVersion
				session.clientCapabilities = initialize.Capabilities
				response.Result = initializeResult(initialize.ProtocolVersion)
				if err := writeResponse(response); err != nil {
					cancelTasks()
					return err
				}
			case "ping":
				response.Result = map[string]any{}
				if err := writeResponse(response); err != nil {
					cancelTasks()
					return err
				}
			case "tools/list":
				if !session.readyForTools() {
					response.Error = newProtocolError(rpcInvalidRequest, "initialize and notifications/initialized must complete before tools/list")
				} else {
					response.Result = map[string]any{"tools": mcpTools()}
				}
				if err := writeResponse(response); err != nil {
					cancelTasks()
					return err
				}
			case "tools/call":
				if !session.readyForTools() {
					response.Error = newProtocolError(rpcInvalidRequest, "initialize and notifications/initialized must complete before tools/call")
					if err := writeResponse(response); err != nil {
						cancelTasks()
						return err
					}
					break
				}
				class := classifyMCPTask(req.Params)
				task := mcpToolTask{
					class: class, phase: mcpTaskQueued, params: append(json.RawMessage(nil), req.Params...),
					requestID: append(json.RawMessage(nil), req.ID...), idKey: idKey, queuedAt: time.Now(),
				}
				if limiter.acquire(class) {
					startTask(task)
					break
				}
				if !limiter.enqueue(class) {
					details := limiter.diagnostics()
					details["phase"] = string(mcpTaskQueued)
					details["task_class"] = string(class)
					response.Result = encodeToolError(newToolError(
						ErrorServerBusy,
						"concurrency",
						"ck3-index has reached its bounded MCP queue capacity",
						true,
						details,
						map[string]any{"guidance": "Retry after active expensive operations finish."},
					), nil)
					if err := writeResponse(response); err != nil {
						cancelTasks()
						return err
					}
					break
				}
				tasks[idKey] = task
				queuedTaskIDs = append(queuedTaskIDs, idKey)
			default:
				response.Error = newProtocolError(rpcMethodNotFound, "method not found")
				if err := writeResponse(response); err != nil {
					cancelTasks()
					return err
				}
			}
		case result := <-taskResults:
			if task, active := tasks[result.idKey]; active {
				if task.cancel != nil {
					task.cancel()
				}
				delete(tasks, result.idKey)
				limiter.release(task.class)
				if !task.cancelled || result.committed {
					if err := writeResponse(result.response); err != nil {
						cancelTasks()
						return err
					}
				}
				dispatchQueued()
			}
		}
	}
	return nil
}

func openMCPDatabase(ctx context.Context, cfg indexer.Config, dbPath string) (*indexer.DB, error) {
	if _, err := os.Stat(dbPath); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		bootstrap, openErr := indexer.OpenWithOptions(dbPath, cfg.SQLiteReadOptions())
		if openErr != nil {
			return nil, openErr
		}
		schemaErr := bootstrap.EnsureSchema(ctx)
		closeErr := bootstrap.Close()
		if schemaErr != nil {
			return nil, schemaErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return indexer.OpenReadOnlyWithOptions(dbPath, cfg.SQLiteReadOptions())
}

func startMCPReader(ctx context.Context, reader *bufio.Reader) <-chan mcpReadEvent {
	events := make(chan mcpReadEvent, 16)
	go func() {
		defer close(events)
		for {
			req, err := readMCPMessage(reader)
			if err == io.EOF {
				return
			}
			event := mcpReadEvent{Request: req, Err: err}
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
			if err != nil {
				var rpcErr *protocolError
				if !errors.As(err, &rpcErr) || rpcErr.Fatal {
					return
				}
			}
		}
	}()
	return events
}

func initializeResult(protocolVersion string) map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"serverInfo":      map[string]any{"name": "ck3-index", "version": buildinfo.Version},
		"instructions":    "CK3 semantic index. When more than one database may be configured, call ck3_database operation=list and select only an exact configured name; never invent or submit a filesystem path. Every result identifies the database lease that supplied its evidence, and a switch affects subsequent calls without rebinding work already running. Begin with ck3_workspace operation=capabilities only when capability selection is uncertain; otherwise use ck3_search to discover an unknown id, ck3_inspect for one exact id, then ck3_prepare_edit, ck3_review, and ck3_preflight for an edit flow. Call ck3_refresh status/files after project source changes; full is explicit and is never substituted silently. Use ck3-index before raw text search; use rg only to inspect exact evidence paths returned by the index. Submit expensive workspace-wide, review, preflight, refresh, packaging, GUI, map-analysis, map-authoring, and raster calls one at a time and await each result. MCP exposes one canonical tool surface; use each tool's bounded operations for precise follow-up.",
		"capabilities": map[string]any{"tools": map[string]any{
			"listChanged": false,
		}},
	}
}

func handleMCPNotification(req rpcRequest, session *mcpSession) (string, bool) {
	if req.JSONRPC != "2.0" || req.Method == "" {
		return "", false
	}
	switch req.Method {
	case "notifications/initialized":
		if session.initialized {
			session.clientInitialized = true
		}
		return "", false
	case "notifications/cancelled":
		idKey, ok := cancelledRequestID(req.Params)
		if !ok {
			return "", false
		}
		return idKey, true
	}
	return "", false
}

func cancelledRequestID(raw json.RawMessage) (string, bool) {
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || len(params.RequestID) == 0 {
		return "", false
	}
	if !validRPCRequestID(params.RequestID) {
		return "", false
	}
	return mcpRequestIDKey(params.RequestID), true
}

func mcpRequestIDKey(raw json.RawMessage) string {
	key, _ := normalizedRPCRequestID(raw)
	return key
}

func runMCPToolTask(
	ctx, sessionCtx context.Context,
	caller mcpToolCaller,
	db *indexer.DB,
	cfg indexer.Config,
	params json.RawMessage,
	requestID json.RawMessage,
	idKey string,
	class mcpTaskClass,
	queuedAt, startedAt time.Time,
	executionTimeout time.Duration,
	releaseDatabase func(),
	results chan<- mcpToolTaskResult,
) {
	if releaseDatabase != nil {
		defer releaseDatabase()
	}
	commitState := &mcpCommitState{}
	ctx = context.WithValue(ctx, mcpCommitStateKey{}, commitState)
	response := rpcResponse{JSONRPC: "2.0", ID: requestID}
	result, err := caller(ctx, db, cfg, params)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && !commitState.committed {
		now := time.Now()
		details := map[string]any{
			"phase":                string(mcpTaskRunning),
			"task_class":           string(class),
			"queue_ms":             startedAt.Sub(queuedAt).Milliseconds(),
			"execution_ms":         now.Sub(startedAt).Milliseconds(),
			"execution_timeout_ms": executionTimeout.Milliseconds(),
		}
		response.Result = encodeToolError(newToolError(
			ErrorOperationTimeout,
			"operation_state",
			"the operation exceeded the MCP execution time limit",
			true,
			details,
			map[string]any{"guidance": "Retry with a narrower request or increase mcp_execution_timeout_seconds."},
		), nil)
	} else if err != nil {
		var rpcErr *protocolError
		if errors.As(err, &rpcErr) {
			response.Error = rpcErr
		} else {
			response.Error = newProtocolError(rpcInternalError, "internal MCP server error")
		}
	} else {
		response.Result = result
	}
	taskResult := mcpToolTaskResult{idKey: idKey, response: response, committed: commitState.committed}
	if commitState.committed {
		results <- taskResult
		return
	}
	select {
	case results <- taskResult:
	case <-sessionCtx.Done():
	}
}

func jsonNullID() []byte {
	return []byte("null")
}

// saveTaskClass grades ck3_save by operation rather than by name: reading a
// header is trivial, streaming a whole gamestate is not.
func saveTaskClass(arguments json.RawMessage) mcpTaskClass {
	var args struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return mcpTaskHeavy
	}
	switch strings.ToLower(strings.TrimSpace(args.Operation)) {
	case "audit", "character":
		return mcpTaskHeavy
	default:
		return mcpTaskRead
	}
}
