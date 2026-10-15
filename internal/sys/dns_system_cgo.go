//go:build android && cgo

package sys

// Android's own resolver (getaddrinfo, which asks netd) is linked in.
const haveSystemDNS = true
