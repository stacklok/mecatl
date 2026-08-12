package main

// version is the mecatui build version shown on the first-run welcome splash and
// threaded into ui.Deps.Version. It defaults to "dev" and is overridden at build
// time via ldflags (see Taskfile.yml: -X main.version=$(git describe ...)).
var version = "dev"
