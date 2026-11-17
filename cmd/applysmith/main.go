// Command applysmith is the headless CLI and server.
package main

import "applysmith/internal/cli"

func main() { cli.Main(cli.Config{Dashboard: dashboardAssets}) }
