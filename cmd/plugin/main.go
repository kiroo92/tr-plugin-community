package main

import (
	"github.com/TokenFlux/TokenRouter/plugin-sdk/go/sdk"
	"github.com/kiroo92/tr-plugin-community/internal/app"
)

func main() { sdk.Serve(app.New()) }
