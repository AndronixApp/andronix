//go:build !(android && cgo)

package sys

// Only Go's resolver: the static in-distro build, builds without the NDK.
const haveSystemDNS = false
