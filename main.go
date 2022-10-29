package main

import (
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adlio/trello"
)

const TRELLO_KEY_ENV = "TRELLO_KEY"
const TRELLO_TOKEN_ENV = "TRELLO_TOKEN"
const TRELLO_ARCHIVES_LISTS_ENV = "TRELLO_ARCHIVE_LISTS"
const TRELLO_REORDER_LISTS_ENV = "TRELLO_REORDER_LISTS"
const CARD_INACTIVITY_ARCHIVAL_THRESHOLD_HOURS_ENV = "CARD_INACTIVITY_ARCHIVAL_THRESHOLD_HOURS"

func extractEnvOrExit(envKey string) string {
	data, defined := os.LookupEnv(envKey)
	if !defined {
		log.Fatalf("ERROR: \"%s\" env var is not defined\n", envKey)
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

func checkCardForArchive(list *trello.List, card *trello.Card, inactivityTimeSpan time.Duration, now time.Time, wg *sync.WaitGroup) {
	defer wg.Done()
	var latestActionTime time.Time = time.UnixMilli(0)

	actions, err := card.GetActions()
	if err != nil {
		log.Panicf("Error during fetching card %v action: %v\n", card.Name, err)
	}

	if actions.Len() == 0 {
		// looking for card creation action though list

		// log.Printf("Card %v(%v) has no actions. Trying find them though board actions\n", card.Name, card.ID)
		var args map[string]string = make(map[string]string)
		args["filter"] = "createCard"
		args["idModels"] = card.ID
		actions, err = list.GetActions(args)

		if err != nil {
			log.Panicf("Error during fetching card %v board action: %v\n", card.Name, err)
		}
		// log.Printf("Got %d actions for card %v via list query", len(actions), card.Name)
	}

	if actions.Len() > 0 {
		for _, action := range actions {
			if action.Data.Card.ID != card.ID {
				log.Printf("skipping action for card %v, as it is not related to card %v\n", action.Data.Card.ID, card.ID)
				continue
			}
			if !(action.DidCreateCard() ||
				action.DidChangeCardMembership() ||
				action.DidChangeListForCard() ||
				action.DidCommentCard()) {
				log.Printf("card %v skipping action %v\n", card.Name, action.Type)
				continue
			}

			curActDate := action.Date
			if latestActionTime.Before(curActDate) {
				latestActionTime = curActDate
			}
		}
	} else {
		log.Printf("Card %v(%v) has no actions\n", card.Name, card.ID)
		latestActionTime = *card.DateLastActivity
	}

	elapsed := now.Sub(latestActionTime)
	if elapsed > inactivityTimeSpan {
		log.Printf("Card \"%v\" (%v) is due to archive as last activity was %v ago\n", card.Name, card.ID, elapsed)
		card.Archive()
	}
}

func tryExtractSimilarity(card *trello.Card) *float64 {
	var spaceDescLastIdx int = strings.LastIndex(card.Desc, " ")
	if spaceDescLastIdx == -1 {
		log.Printf("Can't extract similarity from card (%v) desc\n", card.Name)
		return nil
	}

	toParse := card.Desc[spaceDescLastIdx+1:]
	simVal, err := strconv.ParseFloat(toParse, 64)
	if err == nil {
		return &simVal
	}
	log.Printf("Can't extract similarity from card (%v) desc. can't parse float \"%v\"\n", card.Name, toParse)

	return nil
}

func checkCardForOrder(card *trello.Card, wg *sync.WaitGroup) {
	defer wg.Done()
	cardSim := tryExtractSimilarity(card)
	if cardSim != nil {
		diff := 1.0 - card.Pos*1e-7 - *cardSim
		// log.Printf("card %v pos %v, sim %v, diff %v\n", card.Name, card.Pos, *cardSim, diff)
		if math.Abs(diff) > 1e-2 {
			newPos := (1.0 - *cardSim) * 1e7
			card.SetPos(newPos)
			log.Printf("Changed pos of %v (%v) sim %s to %v\n", card.Name, card.ID, strconv.FormatFloat(*cardSim, 'f', 4, 64), newPos)
		}
	}
}

func fetchList(client *trello.Client, listId string) *trello.List {
	list, err := client.GetList(listId)
	if err != nil {
		log.Panicf("Can't fetch list %v: %v", listId, err)
	}
	return list
}

func main() {
	trelloAppKey := extractEnvOrExit(TRELLO_KEY_ENV)
	trelloToken := extractEnvOrExit(TRELLO_TOKEN_ENV)
	trelloReorderLists := extractEnvOrDefault(TRELLO_REORDER_LISTS_ENV, "")
	trelloArchiveLists := extractEnvOrDefault(TRELLO_ARCHIVES_LISTS_ENV, "")
	cardInactivityArchivalThresholdHoursStr := extractEnvOrDefault(CARD_INACTIVITY_ARCHIVAL_THRESHOLD_HOURS_ENV, "336")
	cardInactivityArchivalThresholdHours, err := strconv.ParseFloat(cardInactivityArchivalThresholdHoursStr, 64)
	if err != nil {
		log.Fatalf("ERROR: can't parse number of card inactivity archival threshold (hours). String: %s \n", cardInactivityArchivalThresholdHoursStr)
	}
	var cardInactivityArchivalThreshold time.Duration = time.Duration(cardInactivityArchivalThresholdHours * 60 * 60 * 1e9)

	client := trello.NewClient(trelloAppKey, trelloToken)

	checkListForCardArchival := func(listId string, wg *sync.WaitGroup) {
		list := fetchList(client, listId)
		log.Printf("Querying cards of the list %v (%v)... \n", listId, list.Name)
		cards, err := list.GetCards()
		if err != nil {
			log.Panicf("Can't fetch cards for %v: %v", list.Name, err)
		}
		log.Printf("The list %v contains %d cards\n", list.Name, len(cards))

		now := time.Now()

		var archivalCheckWg sync.WaitGroup
		archivalCheckWg.Add(len(cards))
		for _, card := range cards {
			go checkCardForArchive(list, card, cardInactivityArchivalThreshold, now, &archivalCheckWg)
		}
		archivalCheckWg.Wait()
		wg.Done()
		log.Printf("List %v processed for card archival", list.Name)
	}

	var wg sync.WaitGroup

	var N int = 0
	if len(trelloArchiveLists) > 0 {
		trelloArchiveListsSplit := strings.Split(trelloArchiveLists, ",")
		N = len(trelloArchiveListsSplit)

		log.Printf("%d lists to check for card archival...\n", N)
		wg.Add(N)
		for _, listId := range trelloArchiveListsSplit {
			go checkListForCardArchival(listId, &wg)
		}
		wg.Wait()
		log.Print("Done with card archival")
	}

	checkListForCardReorder := func(listId string, wg *sync.WaitGroup) {
		list := fetchList(client, listId)
		log.Printf("Querying cards of the list %v (%v)... \n", listId, list.Name)
		cards, err := list.GetCards()
		if err != nil {
			log.Panicf("Can't fetch cards for %v: %v", list.Name, err)
		}
		log.Printf("The list %v contains %d cards\n", list.Name, len(cards))

		var reorderCheckWg sync.WaitGroup
		reorderCheckWg.Add(len(cards))
		for _, card := range cards {
			go checkCardForOrder(card, &reorderCheckWg)
		}
		reorderCheckWg.Wait()
		wg.Done()
		log.Printf("List %v processed for card reorder", list.Name)
	}

	N = 0
	if len(trelloReorderLists) > 0 {
		trelloReorderListsSplit := strings.Split(trelloReorderLists, ",")
		N = len(trelloReorderListsSplit)
		log.Printf("%d lists to check for card reorder...\n", N)
		wg.Add(N)
		for _, listId := range trelloReorderListsSplit {
			go checkListForCardReorder(listId, &wg)
		}
		wg.Wait()
		log.Print("Done with card reorder")
	}

	log.Println("Done")

}
