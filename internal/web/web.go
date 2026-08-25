package web

import (
	"embed"
)

//go:embed dashboard.html
var DashboardFS embed.FS

//go:embed dashboard.html
var DashboardHTML []byte
