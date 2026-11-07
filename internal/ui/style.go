// Package ui holds the visual language shared by the CLI and the TUI.
package ui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	Accent  = lipgloss.AdaptiveColor{Light: "#C2410C", Dark: "#F97316"} // Claude orange
	Green   = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	Red     = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	Yellow  = lipgloss.AdaptiveColor{Light: "#A16207", Dark: "#FACC15"}
	Muted   = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	Surface = lipgloss.AdaptiveColor{Light: "#F3F4F6", Dark: "#1F2937"}

	Title    = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	Faint    = lipgloss.NewStyle().Foreground(Muted)
	Bold     = lipgloss.NewStyle().Bold(true)
	OK       = lipgloss.NewStyle().Foreground(Green)
	Err      = lipgloss.NewStyle().Foreground(Red)
	Warn     = lipgloss.NewStyle().Foreground(Yellow)
	Selected = lipgloss.NewStyle().Background(Surface).Bold(true)
)

// ActiveDot marks the account Claude Code is using.
func ActiveDot(active bool) string {
	if active {
		return OK.Render("●")
	}
	return Faint.Render("○")
}

// Plan renders a subscription plan as a small badge.
func Plan(plan string) string {
	if plan == "" {
		return ""
	}
	return lipgloss.NewStyle().Foreground(Accent).Bold(true).Render(strings.ToUpper(plan))
}

// Expiry renders how long a login stays valid.
func Expiry(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := t.Sub(now)
	switch {
	case d <= 0:
		return Err.Render("expired")
	case d < 72*time.Hour:
		return Warn.Render("expires in " + human(d))
	default:
		return Faint.Render("valid " + human(d))
	}
}

func human(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	case d >= time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Minutes())) + "m"
	}
}
