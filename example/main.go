// Command example shows a minimal Helios integration.
//
//	HELIOS_SDK_KEY=... HELIOS_BASE_URL=https://api.helios.example.com go run ./example
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	helios "github.com/bhavyamsharmaa/helios-go"
)

func main() {
	client, err := helios.New(os.Getenv("HELIOS_SDK_KEY")) // base URL comes from HELIOS_BASE_URL
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	user := helios.Subject{Key: "user-123"}
	if client.BoolVariation(ctx, "new-checkout", user, false) {
		fmt.Println("showing the new checkout")
	} else {
		fmt.Println("showing the old checkout")
	}

	// Optional: react to changes (such as a kill switch) as they happen.
	fmt.Println("listening for flag updates, Ctrl-C to stop")
	err = client.Subscribe(ctx, func(u helios.FlagUpdate) {
		fmt.Printf("flag %q changed: enabled=%v version=%d\n", u.FlagKey, u.Enabled, u.Version)
	})
	if err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
