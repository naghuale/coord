package agentsession

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func IdempotencyKey(operation, projectID, assignmentID, sessionID string, generation, sequence uint64, leaseDigest string) string {
	material := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%s", operation, projectID, assignmentID, sessionID, generation, sequence, leaseDigest)
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}
