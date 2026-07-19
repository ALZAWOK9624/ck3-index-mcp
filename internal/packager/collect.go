package packager

import (
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var excludedDirectoryNames = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".idea": true, ".vscode": true,
	"cache": true, "tmp": true, "logs": true, "node_modules": true, "bin": true, "obj": true,
}

var excludedExtensions = map[string]bool{
	".zip": true, ".7z": true, ".rar": true, ".tar": true, ".gz": true,
	".sqlite": true, ".db": true, ".log": true, ".bak": true, ".tmp": true, ".temp": true,
}

func RequestFromDirectory(root string, metadata Metadata, limits Limits) (Request, []string, error) {
	if strings.TrimSpace(root) == "" {
		return Request{}, nil, fmt.Errorf("mod directory is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return Request{}, nil, err
	}
	if !info.IsDir() {
		return Request{}, nil, fmt.Errorf("mod directory is not a directory: %s", root)
	}
	if limits.MaxFiles <= 0 {
		limits = DirectoryLimits
	}
	var request Request
	request.Metadata = metadata
	var excluded []string
	var total int64
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepathSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not allowed in package input: %s", rel)
		}
		parts := strings.Split(rel, "/")
		if len(parts) == 1 {
			lowerName := strings.ToLower(entry.Name())
			if entry.IsDir() && !allowedRoots[lowerName] {
				excluded = append(excluded, rel+"/")
				return fs.SkipDir
			}
			if !entry.IsDir() && lowerName != "descriptor.mod" && !strings.HasSuffix(lowerName, ".mod") && lowerName != "thumbnail.png" {
				excluded = append(excluded, rel)
				return nil
			}
		}
		if entry.IsDir() {
			if excludedDirectoryNames[strings.ToLower(entry.Name())] {
				excluded = append(excluded, rel+"/")
				return fs.SkipDir
			}
			return nil
		}
		if excludedExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			excluded = append(excluded, rel)
			return nil
		}
		if len(request.Files) >= limits.MaxFiles {
			return fmt.Errorf("directory package exceeds %d files", limits.MaxFiles)
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if fileInfo.Size() > limits.MaxFileBytes {
			return fmt.Errorf("file %q exceeds %d bytes", rel, limits.MaxFileBytes)
		}
		total += fileInfo.Size()
		if total > limits.MaxTotalBytes {
			return fmt.Errorf("directory package exceeds %d decoded bytes", limits.MaxTotalBytes)
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		if directoryTextFile(rel) {
			content := string(data)
			request.Files = append(request.Files, FileInput{Path: rel, Content: &content})
		} else {
			encoded := base64.StdEncoding.EncodeToString(data)
			request.Files = append(request.Files, FileInput{Path: rel, ContentBase64: &encoded})
		}
		return nil
	})
	if err != nil {
		return Request{}, excluded, err
	}
	sort.Slice(request.Files, func(i, j int) bool { return request.Files[i].Path < request.Files[j].Path })
	sort.Strings(excluded)
	return request, excluded, nil
}

func directoryTextFile(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".txt", ".gui", ".yml", ".yaml", ".csv", ".map", ".mod", ".asset", ".settings", ".json", ".md":
		return true
	default:
		return false
	}
}
