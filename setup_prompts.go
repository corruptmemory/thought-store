package main

import (
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

var (
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).PaddingTop(1).PaddingBottom(1)
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

func printHeader(msg string) {
	fmt.Fprintln(os.Stderr, headerStyle.Render(msg))
}

func printSuccess(msg string) {
	fmt.Fprintln(os.Stderr, successStyle.Render("  \u2713 "+msg))
}

func printWarn(msg string) {
	fmt.Fprintln(os.Stderr, warnStyle.Render("  ! "+msg))
}

func printError(msg string) {
	fmt.Fprintln(os.Stderr, errorStyle.Render("  \u2717 "+msg))
}

func confirmPrompt(title string, defaultVal bool) (bool, error) {
	result := defaultVal
	err := huh.NewConfirm().
		Title(title).
		Affirmative("Yes").
		Negative("No").
		Value(&result).
		WithTheme(huh.ThemeBase()).
		Run()
	if err != nil {
		return false, err
	}
	return result, nil
}

func inputPrompt(title, defaultVal string) (string, error) {
	var result string
	err := huh.NewInput().
		Title(title).
		Placeholder(defaultVal).
		Value(&result).
		WithTheme(huh.ThemeBase()).
		Run()
	if err != nil {
		return "", err
	}
	if result == "" {
		return defaultVal, nil
	}
	return result, nil
}

func selectPrompt(title string, options []string) (string, error) {
	var result string
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, o)
	}
	err := huh.NewSelect[string]().
		Title(title).
		Options(opts...).
		Value(&result).
		WithTheme(huh.ThemeBase()).
		Run()
	if err != nil {
		return "", err
	}
	return result, nil
}
