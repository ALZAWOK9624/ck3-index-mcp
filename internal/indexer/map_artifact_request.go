package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const maxArtifactRequestKeyBytes = 128

var artifactRequestKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// NormalizeArtifactRequestKey validates the caller's idempotency key before it
// can be persisted in a manifest or used to derive a controlled artifact id.
func NormalizeArtifactRequestKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > maxArtifactRequestKeyBytes || !artifactRequestKeyPattern.MatchString(value) {
		return "", fmt.Errorf("request_key must be 1..%d ASCII letters, digits, dot, underscore, colon, or hyphen", maxArtifactRequestKeyBytes)
	}
	return value, nil
}

func artifactRequestSHA256(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func idempotentArtifactID(prefix, operation, requestKey string) string {
	digest := sha256.Sum256([]byte(operation + "\x00" + requestKey))
	return prefix + hex.EncodeToString(digest[:16])
}
