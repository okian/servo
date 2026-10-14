// This file is untagged on purpose: go generate honours build constraints,
// so a directive inside either tagged spec file would never run. One line
// per variant, since each configuration needs its own generation — the
// shape `servo init --tags=prod` scaffolds.
package main

//go:generate go tool servo generate
//go:generate go tool servo generate --tags=prod
