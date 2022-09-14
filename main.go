package main

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/adlio/trello"
)

const TRELLO_KEY_ENV = "TRELLO_KEY"
const TRELLO_TOKEN_ENV = "TRELLO_TOKEN"
const TRELLO_LIST_ENV = "TRELLO_LIST"
const CARD_INACTIVITY_ARCHIVAL_THRESHOLD_HOURS_ENV = "CARD_INACTIVITY_ARCHIVAL_THRESHOLD_HOURS"

func extractEnvOrExit(envKey string) string {
	data, defined := os.LookupEnv(envKey)
	if !defined {
		fmt.Printf("ERROR: \"%s\" env var is not defined\n", envKey)
		os.Exit(1)
	}
	return data
}

func extractEnvOrDefault(envKey string, defaultVal string) string {
	data, defined := os.LookupEnv(envKey)
	if !defined {
		return defaultVal
	}
	return data
}

func checkCardForArchive(card *trello.Card, inactivityTimeSpan time.Duration, now time.Time, wg *sync.WaitGroup) error {
	defer wg.Done()
	elapsed := now.Sub(*card.DateLastActivity)
	if elapsed > inactivityTimeSpan {
		fmt.Printf("Card \"%v\" is due to archive as last activity was %v ago\n", card.Name, elapsed)
		return card.Archive()
	}
	return nil
}

func main() {
	trelloAppKey := extractEnvOrExit(TRELLO_KEY_ENV)
	trelloToken := extractEnvOrExit(TRELLO_TOKEN_ENV)
	//trelloAppMemberId := extractEnvOrExit(TRELLO_APP_MEMBER_ID_ENV)
	trelloList := extractEnvOrExit(TRELLO_LIST_ENV)
	cardInactivityArchivalThesholdHoursStr := extractEnvOrDefault(CARD_INACTIVITY_ARCHIVAL_THRESHOLD_HOURS_ENV, "336")
	cardInactivityArchivalThesholdHours, err := strconv.ParseFloat(cardInactivityArchivalThesholdHoursStr, 64)
	if err != nil {
		fmt.Printf("ERROR: can't parse number of card inactivity archival threshold (hours). String: %s \n", cardInactivityArchivalThesholdHoursStr)
	}
	var cardInactivityArchivalThreshold time.Duration = time.Duration(cardInactivityArchivalThesholdHours * 60 * 60 * 1e9)

	fmt.Printf("Querying cards of the list %v... \n", trelloList)

	client := trello.NewClient(trelloAppKey, trelloToken)

	list, err := client.GetList(trelloList)
	if err != nil {
		fmt.Printf("Can't fetch list %v: %v", trelloList, err)
		os.Exit(2)
	}
	cards, err := list.GetCards()
	if err != nil {
		fmt.Printf("Can't fetch cards for %v: %v", list.Name, err)
		os.Exit(2)
	}
	fmt.Printf("The list %v contains %d cards\n", list.Name, len(cards))

	now := time.Now()

	var archivalCheckWg sync.WaitGroup
	archivalCheckWg.Add(len(cards))
	for _, card := range cards {
		go checkCardForArchive(card, cardInactivityArchivalThreshold, now, &archivalCheckWg)
	}
	archivalCheckWg.Wait()
	fmt.Println("Done")
}
