package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/sasho/calendar-availability-proxy/internal/icloud"
)

func main() {
	email := os.Getenv("ICLOUD_EMAIL")
	pass := os.Getenv("ICLOUD_APP_PASSWORD")
	base := os.Getenv("CALDAV_BASE_URL")
	if base == "" {
		base = icloud.DefaultCalDAVBase
	}
	if email == "" || pass == "" {
		log.Fatal("set ICLOUD_EMAIL and ICLOUD_APP_PASSWORD")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	c, err := icloud.NewCalDAVClient(email, pass, base)
	if err != nil {
		log.Fatal(err)
	}
	cals, err := icloud.ListCalendars(ctx, c)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Path\tName")
	for _, cal := range cals {
		fmt.Printf("%s\t%s\n", cal.Path, cal.Name)
	}
}
