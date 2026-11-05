//go:build !windows

package upstream

// killChildrenWithParent has nothing to do here. A stdio child sees its input
// close when vaultd dies, which is how MCP servers know to exit.
func killChildrenWithParent() error { return nil }
