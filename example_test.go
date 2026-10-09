package helios_test

import (
	"context"
	"fmt"
	"log"
	"os"

	helios "github.com/bhavyamsharmaa/helios-go"
)

func ExampleNew() {
	client, err := helios.New(os.Getenv("HELIOS_SDK_KEY"),
		helios.WithBaseURL("https://api.helios.example.com"))
	if err != nil {
		log.Fatal(err)
	}
	_ = client
}

func ExampleClient_BoolVariation() {
	client, err := helios.New("sdk-key", helios.WithBaseURL("https://api.helios.example.com"))
	if err != nil {
		log.Fatal(err)
	}

	user := helios.Subject{Key: "user-123", Attributes: map[string]any{"plan": "pro"}}
	// The last argument is returned if Helios cannot be reached.
	if client.BoolVariation(context.Background(), "new-checkout", user, false) {
		fmt.Println("new checkout")
	}
}

func ExampleClient_Subscribe() {
	client, err := helios.New("sdk-key", helios.WithBaseURL("https://api.helios.example.com"))
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		err := client.Subscribe(ctx, func(u helios.FlagUpdate) {
			fmt.Printf("flag %s changed (enabled=%v)\n", u.FlagKey, u.Enabled)
		})
		log.Println("stream stopped:", err)
	}()
}
