package indexer

import (
	"context"
	"database/sql"
	"strings"
)

const (
	objectInsertBatchSize       = 64
	referenceInsertBatchSize    = 64
	objectFieldInsertBatchSize  = 64
	localizationInsertBatchSize = 128
	nameInsertBatchSize         = 128
)

func multiRowInsertSQL(prefix, row string, count int) string {
	var builder strings.Builder
	builder.Grow(len(prefix) + count*(len(row)+1))
	builder.WriteString(prefix)
	for index := 0; index < count; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(row)
	}
	return builder.String()
}

func insertObjectRows(ctx context.Context, tx *sql.Tx, rows []objectRow) error {
	const prefix = `INSERT INTO objects(object_type,name,value,file_id,node_local_id,source_name,source_rank,path,line,col,end_line,end_col) VALUES `
	const values = `(?,?,?,?,?,?,?,?,?,?,?,?)`
	for start := 0; start < len(rows); start += objectInsertBatchSize {
		end := min(start+objectInsertBatchSize, len(rows))
		args := make([]any, 0, (end-start)*12)
		for _, row := range rows[start:end] {
			args = append(args, row.Type, row.Name, row.Value, row.FileID, row.NodeID, row.SourceName, row.SourceRank, row.Path, row.Line, row.Col, row.EndLine, row.EndCol)
		}
		if _, err := tx.ExecContext(ctx, multiRowInsertSQL(prefix, values, end-start), args...); err != nil {
			return err
		}
	}
	return nil
}

func insertReferenceRows(ctx context.Context, tx *sql.Tx, rows []refRow) error {
	const prefix = `INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,node_local_id,line,col,raw,resolved,relation,phase,confidence,resolution_reason) VALUES `
	const values = `(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	for start := 0; start < len(rows); start += referenceInsertBatchSize {
		end := min(start+referenceInsertBatchSize, len(rows))
		args := make([]any, 0, (end-start)*14)
		for _, row := range rows[start:end] {
			args = append(args, row.FromType, row.FromName, row.Kind, row.Name, row.FileID, row.NodeID, row.Line, row.Col, row.Raw, row.Resolved, row.Relation, row.Phase, row.Confidence, row.ResolutionReason)
		}
		if _, err := tx.ExecContext(ctx, multiRowInsertSQL(prefix, values, end-start), args...); err != nil {
			return err
		}
	}
	return nil
}

func insertObjectFieldRows(ctx context.Context, tx *sql.Tx, rows []objectFieldRow) error {
	const prefix = `INSERT INTO object_fields(object_type,object_name,field,value_shape,date_key,file_id,source_name,source_rank,path,line,raw) VALUES `
	const values = `(?,?,?,?,?,?,?,?,?,?,?)`
	for start := 0; start < len(rows); start += objectFieldInsertBatchSize {
		end := min(start+objectFieldInsertBatchSize, len(rows))
		args := make([]any, 0, (end-start)*11)
		for _, row := range rows[start:end] {
			args = append(args, row.Type, row.ObjectName, row.Field, row.Shape, row.DateKey, row.FileID, row.SourceName, row.SourceRank, row.Path, row.Line, row.Raw)
		}
		if _, err := tx.ExecContext(ctx, multiRowInsertSQL(prefix, values, end-start), args...); err != nil {
			return err
		}
	}
	return nil
}

func insertLocalizationRows(ctx context.Context, tx *sql.Tx, record fileRecord, rows []locEntry) error {
	const prefix = `INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES `
	const values = `(?,?,?,?,?,?,?,?,?)`
	for start := 0; start < len(rows); start += localizationInsertBatchSize {
		end := min(start+localizationInsertBatchSize, len(rows))
		args := make([]any, 0, (end-start)*9)
		for _, row := range rows[start:end] {
			args = append(args, row.key, row.lang, row.val, record.ID, record.SourceName, record.SourceRank, record.Path, row.line, row.replace)
		}
		if _, err := tx.ExecContext(ctx, multiRowInsertSQL(prefix, values, end-start), args...); err != nil {
			return err
		}
	}
	return nil
}

func insertSavedScopes(ctx context.Context, tx *sql.Tx, fileID int64, names []string) error {
	return insertFileNames(ctx, tx, `INSERT INTO saved_scopes(file_id,scope_name) VALUES `, fileID, names)
}

func insertVariables(ctx context.Context, tx *sql.Tx, fileID int64, names []string) error {
	return insertFileNames(ctx, tx, `INSERT INTO variables(file_id,var_name) VALUES `, fileID, names)
}

func insertFileNames(ctx context.Context, tx *sql.Tx, prefix string, fileID int64, names []string) error {
	const values = `(?,?)`
	for start := 0; start < len(names); start += nameInsertBatchSize {
		end := min(start+nameInsertBatchSize, len(names))
		args := make([]any, 0, (end-start)*2)
		for _, name := range names[start:end] {
			args = append(args, fileID, name)
		}
		if _, err := tx.ExecContext(ctx, multiRowInsertSQL(prefix, values, end-start), args...); err != nil {
			return err
		}
	}
	return nil
}
