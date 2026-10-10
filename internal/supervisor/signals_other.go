//go:build !unix

package supervisor

import "context"

func (s *Loop) RunWithSignals(ctx context.Context, req RunRequest) (Receipt, error) {
	return s.Run(ctx, req)
}
