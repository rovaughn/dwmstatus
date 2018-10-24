package main

import (
	"bytes"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

var acpiRe = regexp.MustCompile(`Battery 0: (Charging|Discharging), (\d+)%, (\d+):(\d+):(\d+)`)

type power struct {
	charging   bool
	percentage int
	remaining  time.Duration
}

func getPower() (*power, error) {
	out, err := exec.Command("acpi").Output()
	if err != nil {
		return nil, err
	}

	m := acpiRe.FindSubmatch(out)
	if m == nil {
		return nil, fmt.Errorf("acpi returned unexpected output: %q", out)
	}

	status := m[1]

	percentage, err := strconv.Atoi(string(m[2]))
	if err != nil {
		return nil, err
	}

	hours, err := strconv.Atoi(string(m[3]))
	if err != nil {
		return nil, err
	}

	minutes, err := strconv.Atoi(string(m[4]))
	if err != nil {
		return nil, err
	}

	seconds, err := strconv.Atoi(string(m[5]))
	if err != nil {
		return nil, err
	}

	return &power{
		charging:   bytes.Equal(status, []byte("Charging")),
		percentage: percentage,
		remaining:  time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second,
	}, nil
}

func update() {
	var group sync.WaitGroup
	var powerText, timeText string

	group.Add(1)
	go func() {
		defer group.Done()
		power, err := getPower()
		if err == nil {
			if power.charging {
				powerText = fmt.Sprintf("charging %d%% (%.0fm)", power.percentage, power.remaining.Seconds()/60)
			} else {
				powerText = fmt.Sprintf("discharging %d%% (%.0fm)", power.percentage, power.remaining.Seconds()/60)
			}
		} else {
			powerText = "(err)"
			log.Printf("Getting power: %s", err)
		}
	}()

	group.Add(1)
	go func() {
		defer group.Done()
		timeText = time.Now().Format("Mon 2 Jan 2006 15:04:05 -0700 MST")
	}()

	group.Wait()

	text := fmt.Sprintf("%s | %s", powerText, timeText)

	if err := exec.Command("xsetroot", "-name", text).Run(); err != nil {
		log.Printf("xsetroot: %s", err)
	}
}

func main() {
	log.Printf("Starting")

	go update()

	const interval = 5 * time.Second

	now := time.Now()
	start := time.Now().Round(interval)
	if start.Before(now) {
		start = start.Add(interval)
	}
	time.Sleep(start.Sub(now))

	go update()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		go update()
	}
}
