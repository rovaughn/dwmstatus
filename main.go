package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// eagerTick is like time.Tick, but it also includes a tick that fires
// immediately.
func eagerTick(interval time.Duration) <-chan time.Time {
	ch := make(chan time.Time)
	go func() {
		ch <- time.Now()
		for t := range time.Tick(interval) {
			ch <- t
		}
	}()
	return ch
}

func debounce(ch <-chan time.Time, interval time.Duration) <-chan time.Time {
	outCh := make(chan time.Time)

	go func() {
		var lastTime time.Time
		var timer *time.Timer
		var timerCh <-chan time.Time

		for {
			select {
			case t := <-ch:
				lastTime = t
				if timer != nil {
					timer.Stop()
					// TODO if the timer already fired before we stop it, I think this
					// could leak.  We might need to drain timer.C if timer.Stop()
					// returns false.
				}
				timer = time.NewTimer(interval)
				timerCh = timer.C
			case <-timerCh:
				outCh <- lastTime
			}
		}
	}()

	return outCh
}

func thermalLoop(ch chan<- string) {
	re := regexp.MustCompile(`Thermal 0: ok, ([.0-9]+) degrees F`)

	for range eagerTick(time.Second) {
		out, err := exec.Command("acpi", "--thermal", "--fahrenheit").Output()
		if err != nil {
			log.Print(err)
			ch <- "(err)"
			continue
		}

		m := re.FindSubmatch(out)
		if m == nil {
			log.Printf("acpi returned unexpected output: %q", out)
			ch <- "(err)"
			continue
		}

		tempF, err := strconv.ParseFloat(string(m[1]), 64)
		if err != nil {
			log.Print(err)
			ch <- "(err)"
			continue
		}

		if tempF >= 95 {
			ch <- fmt.Sprintf("\x04%.1f \u00b0F", tempF)
		} else {
			ch <- fmt.Sprintf("%.1f \u00b0F", tempF)
		}
	}
}

func powerLoop(ch chan<- string) {
	updateCh := make(chan time.Time)

	go func() {
		// Whenever upower --monitor detects a change, we'll want to update the
		// power text.
		cmd := exec.Command("upower", "--monitor")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Print(err)
			return
		}
		defer stdout.Close()
		if err := cmd.Start(); err != nil {
			log.Print(err)
			return
		}

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			updateCh <- time.Now()
		}
	}()

	go func() {
		for t := range eagerTick(time.Minute) {
			updateCh <- t
		}
	}()

	re := regexp.MustCompile(`Battery 0: (Unknown|Charging|Discharging), (\d+)%(, (\d+):(\d+):(\d+))?`)

	for range debounce(updateCh, time.Second) {
		out, err := exec.Command("acpi", "--battery").Output()
		if err != nil {
			log.Print(err)
			ch <- "(err)"
			continue
		}

		if bytes.Equal(out, []byte("Battery 0: Full, 100%\n")) {
			ch <- "charged 100%"
			continue
		}

		m := re.FindSubmatch(out)
		if m == nil {
			log.Printf("acpi returned unexpected output: %q", out)
			ch <- "(err)"
			continue
		}

		status := string(m[1])

		percentage, err := strconv.Atoi(string(m[2]))
		if err != nil {
			log.Print(err)
			ch <- "(err)"
			continue
		}

		var remaining time.Duration

		if m[3] != nil {
			hours, err := strconv.Atoi(string(m[4]))
			if err != nil {
				log.Print(err)
				ch <- "(err)"
				continue
			}

			minutes, err := strconv.Atoi(string(m[5]))
			if err != nil {
				log.Print(err)
				ch <- "(err)"
				continue
			}

			seconds, err := strconv.Atoi(string(m[6]))
			if err != nil {
				log.Print(err)
				ch <- "(err)"
				continue
			}

			remaining = time.Duration(hours)*time.Hour +
				time.Duration(minutes)*time.Minute +
				time.Duration(seconds)*time.Second
		}

		totalMinutes := int(remaining.Seconds() / 60)
		remainingText := fmt.Sprintf("%dh%02dm", totalMinutes/60, totalMinutes%60)

		switch status {
		case "Charging":
			ch <- fmt.Sprintf("charging %d%% (%s)", percentage, remainingText)
		case "Discharging":
			if percentage <= 20 {
				ch <- fmt.Sprintf("\x04discharging %d%% (%s)", percentage, remainingText)
			} else {
				ch <- fmt.Sprintf("discharging %d%% (%s)", percentage, remainingText)
			}
		case "Unknown":
			ch <- fmt.Sprintf("unknown %d%%", percentage)
		}
	}
}

func timeLoop(ch chan<- string) {
	for now := range eagerTick(time.Minute) {
		ch <- now.Format("Mon 2 Jan 2006 3:04 pm -0700 MST")
	}
}

func memoryLoop(ch chan<- string) {
	var re = regexp.MustCompile(`(.*): +(\d+) kB`)
	for range eagerTick(time.Second) {
		data, err := ioutil.ReadFile("/proc/meminfo")
		if err != nil {
			log.Print(err)
			ch <- "(err)"
		}

		var total, available float64

		for _, line := range bytes.Split(data, []byte("\n")) {
			m := re.FindSubmatch(line)
			if m == nil {
				continue
			}

			name := string(m[1])
			kB, err := strconv.Atoi(string(m[2]))
			if err != nil {
				continue
			}

			MB := float64(kB) / 1000

			if name == "MemTotal" {
				total = MB
			} else if name == "MemAvailable" {
				available = MB
			}
		}

		ch <- fmt.Sprintf("RAM: %.0f%%", 100*float64(total-available)/float64(total))
	}
}

func main() {
	log.Printf("Starting")

	loopFuncs := []func(chan<- string){
		powerLoop,
		thermalLoop,
		memoryLoop,
		timeLoop,
	}

	type update struct {
		index int
		text  string
	}

	updateCh := make(chan update)

	for i, f := range loopFuncs {
		i := i
		ch := make(chan string)
		go f(ch)
		go func() {
			for s := range ch {
				updateCh <- update{
					index: i,
					text:  s,
				}
			}
		}()
	}

	chunks := make([]string, len(loopFuncs))
	for i := range chunks {
		chunks[i] = "..."
	}

	oldText := ""

	for update := range updateCh {
		chunks[update.index] = update.text

		newText := strings.Join(chunks, "\x01 | ")

		if newText != oldText {
			if err := exec.Command("xsetroot", "-name", newText).Run(); err != nil {
				log.Printf("xsetroot: %s", err)
			}
			oldText = newText
		}
	}
}
