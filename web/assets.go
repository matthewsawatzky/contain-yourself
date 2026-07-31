// Package web embeds controller templates and static assets.
package web

import "embed"

// Assets contains the complete server-rendered UI.
//
//go:embed templates/*.html static/*
var Assets embed.FS
