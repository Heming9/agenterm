package installer

import _ "embed"

//go:embed install.sh
var Shell []byte

//go:embed install.ps1
var PowerShell []byte
