package main

import _ "embed"

//go:embed icons/menu-bar.png
var menuBarIcon []byte

func trayIcon() []byte { return menuBarIcon }
