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
const TRELLO_DELETE_LISTS_ENV = "TRELLO_DELETE_LISTS"
const TRELLO_ARCHIVES_LISTS_ENV = "TRELLO_ARCHIVE_LISTS"
const TRELLO_REORDER_LISTS_ENV = "TRELLO_REORDER_LISTS"
const CARD_INACTIVITY_THRESHOLD_HOURS_ENV = "CARD_INACTIVITY_THRESHOLD_HOURS"

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

type staleCardActionEnum int32

const (
	staleCardActionDelete staleCardActionEnum = iota + 1
	staleCardActionArchive
)

func checkCardForStaleness(list *trello.List, card *trello.Card, inactivityTimeSpan time.Duration, now time.Time, wg *sync.WaitGroup, staleAction staleCardActionEnum) {
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
		log.Printf("Card \"%v\" (%v) is due to stale action as last activity was %v ago\n", card.Name, card.ID, elapsed)
		switch staleAction {
		case staleCardActionDelete:
			card.Delete()
		case staleCardActionArchive:
			card.Archive()
		default:
			log.Panicf("Unsupported stale card action: %v", staleAction)
		}
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

// Splits the "commaSepListId" by comma to get list ids.
// For each listId applies listAction as gorotine.
// Waits until all of the lists processing complete
func processLists(commaSepListId string, processingDescription string, listAction func(listId string, wg *sync.WaitGroup)) {
	var wg sync.WaitGroup

	listIdsSplit := strings.Split(commaSepListId, ",")
	var N = len(listIdsSplit)

	log.Printf("%d lists to check for %s...\n", N, processingDescription)
	wg.Add(N)
	for _, listId := range listIdsSplit {
		go listAction(listId, &wg)
		//go checkListForStaleCards(listId, &wg, staleCardActionArchive)
	}
	wg.Wait()
	log.Printf("Done with %s\n", processingDescription)
}

func main() {
	trelloAppKey := extractEnvOrExit(TRELLO_KEY_ENV)
	trelloToken := extractEnvOrExit(TRELLO_TOKEN_ENV)
	trelloReorderLists := extractEnvOrDefault(TRELLO_REORDER_LISTS_ENV, "")
	trelloArchiveLists := extractEnvOrDefault(TRELLO_ARCHIVES_LISTS_ENV, "")
	trelloDeleteLists := extractEnvOrDefault(TRELLO_DELETE_LISTS_ENV, "")
	cardInactivityThresholdHoursStr := extractEnvOrDefault(CARD_INACTIVITY_THRESHOLD_HOURS_ENV, "336")
	cardInactivityThresholdHours, err := strconv.ParseFloat(cardInactivityThresholdHoursStr, 64)
	if err != nil {
		log.Fatalf("ERROR: can't parse number of card inactivity threshold (hours). String: %s \n", cardInactivityThresholdHoursStr)
	}
	var cardInactivityThreshold time.Duration = time.Duration(cardInactivityThresholdHours * 60 * 60 * 1e9)

	client := trello.NewClient(trelloAppKey, trelloToken)

	checkListForStaleCards := func(listId string, wg *sync.WaitGroup, staleCardAction staleCardActionEnum) {
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
			go checkCardForStaleness(
				list, card, cardInactivityThreshold, now,
				&archivalCheckWg,
				staleCardAction)
		}
		archivalCheckWg.Wait()
		wg.Done()
		log.Printf("List %v processed for stale cards", list.Name)
	}

	if len(trelloArchiveLists) > 0 {
		processLists(
			trelloArchiveLists,
			"stale cards archival",
			func(listId string, wg *sync.WaitGroup) {
				checkListForStaleCards(listId, wg, staleCardActionArchive)
			})
	}

	if len(trelloDeleteLists) > 0 {
		processLists(
			trelloDeleteLists,
			"stale cards delete",
			func(listId string, wg *sync.WaitGroup) {
				checkListForStaleCards(listId, wg, staleCardActionDelete)
			})
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

	if len(trelloReorderLists) > 0 {
		processLists(
			trelloReorderLists,
			"cards reorder",
			checkListForCardReorder)
	}

	log.Println("Done")

}
