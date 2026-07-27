package main

import "github.com/michaelquigley/push/build"

func init() {
	rootCmd.AddCommand(build.NewVersionCmd("scry"))
}
