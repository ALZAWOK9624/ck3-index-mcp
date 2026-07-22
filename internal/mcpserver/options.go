package mcpserver

import "ck3-index/internal/indexer"

// configureRuntimeOptions attaches the normalized source privacy policy to a
// request. Indexer calls remain usable without an MCP runtime, while public
// MCP requests filter by Source.Private rather than by a source name or rank.
func configureRuntimeOptions(runtime *Runtime, opts indexer.LLMOptions) indexer.LLMOptions {
	if runtime == nil {
		return opts
	}
	if policy := indexer.PrivateSourceNames(runtime.Config); policy != nil {
		opts.PrivateSources = policy
	}
	return opts
}
