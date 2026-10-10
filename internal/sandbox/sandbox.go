package sandbox

import "context"

type Profile struct {
	Name                                                          string
	NetworkDeny, RequireBubblewrap, RequireSeccomp, RequireCgroup bool
}
type Launch struct {
	ProjectRoot, RepositoryRoot, AssignedWorktree, ScratchRoot, Executable string
	Args, Environment                                                      []string
}
type Backend interface {
	Command(Profile, Launch) ([]string, []string, error)
	Validate(Profile) error
}
type SeccompProfile struct {
	Name              string
	DefaultErrno      uint16
	Allow, DenyIntent []string
}
type SeccompLoader interface{ Load(SeccompProfile) error }
type Limits struct{ MemoryMax, MemorySwapMax, CPUQuotaMicros, PIDsMax int64 }
type Cgroup interface {
	Create(context.Context, string, Limits) error
	AddProcess(context.Context, string, int) error
	Remove(context.Context, string) error
}
