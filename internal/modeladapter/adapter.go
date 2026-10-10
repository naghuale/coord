package modeladapter

import (
	"context"
	"time"
)

type Identity struct{ ID, Version, Digest, InterfaceVersion, ModelProfile string }
type Request struct {
	ProjectID, AssignmentID, SubjectSHA, ActionContextDigest, OutputSchemaDigest string
	Input                                                                        []byte
	Timeout                                                                      time.Duration
	RealSideEffects                                                              bool
}
type Usage struct {
	InputTokens, OutputTokens int64
	EstimatedCost             float64
	Currency                  string
}
type Result struct {
	Output               []byte
	OutputDigest, Status string
	Usage                Usage
}
type Adapter interface {
	Identity() Identity
	Invoke(context.Context, Request) (Result, error)
}
