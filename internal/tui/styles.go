
package tui

import "github.com/charmbracelet/lipgloss"

var (
	
	brandPurple = lipgloss.Color("99")
	dimText     = lipgloss.Color("242")
	mutedText   = lipgloss.Color("245")
	normalText  = lipgloss.Color("252")
	brightText  = lipgloss.Color("255")
	greenText   = lipgloss.Color("78")
	redText     = lipgloss.Color("203")
	yellowText  = lipgloss.Color("214")
	cyanText    = lipgloss.Color("80")

	
	bannerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("208")).
			Bold(true)

	bannerDimStyle = lipgloss.NewStyle().
			Foreground(dimText)

	
	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("236"))

	
	promptStyle = lipgloss.NewStyle().
			Foreground(cyanText).
			Bold(true)

	
	aiMarkerStyle = lipgloss.NewStyle().
			Foreground(brandPurple).
			Bold(true)

	
	aiTextStyle = lipgloss.NewStyle().
			Foreground(normalText).
			PaddingLeft(2)

	
	streamingTextStyle = lipgloss.NewStyle().
				Foreground(normalText).
				PaddingLeft(2)

	

	toolRunningStyle = lipgloss.NewStyle().
				Foreground(dimText).
				PaddingLeft(2)

	toolDoneStyle = lipgloss.NewStyle().
			Foreground(greenText).
			PaddingLeft(2)

	toolErrorStyle = lipgloss.NewStyle().
			Foreground(redText).
			PaddingLeft(2)

	toolDetailStyle = lipgloss.NewStyle().
			Foreground(dimText).
			PaddingLeft(4)

	
	errorStyle = lipgloss.NewStyle().
			Foreground(redText).
			PaddingLeft(2)

	
	permBorderStyle = lipgloss.NewStyle().
			Foreground(yellowText).
			Bold(true)

	permDimStyle = lipgloss.NewStyle().
			Foreground(dimText)

	
	statusBarStyle = lipgloss.NewStyle().
			Foreground(dimText)

	statusItemStyle = lipgloss.NewStyle().
			Foreground(mutedText)

	
	selectLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(brandPurple).
				Align(lipgloss.Center)

	selectedItemStyle = lipgloss.NewStyle().
				Foreground(cyanText).
				Bold(true)

	normalItemStyle = lipgloss.NewStyle().
			Foreground(mutedText)
)

