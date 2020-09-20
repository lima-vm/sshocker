package mount

type MountType = int

const (
	MountTypeInvalid MountType = iota
	MountTypeReverseSSHFS
)

type Mount struct {
	Type        MountType
	Source      string
	Destination string
	Readonly    bool
}
