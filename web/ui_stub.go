//go:build !ui

package web

import "io/fs"

func FS() fs.FS { return nil }

const Built = false
