//go:build tools

// Keeps golang.org/x/mobile in go.mod: `gomobile bind` needs golang.org/x/mobile/bind
// to be a module dependency, and go mod tidy would otherwise drop it.
package tools

import _ "golang.org/x/mobile/bind"
