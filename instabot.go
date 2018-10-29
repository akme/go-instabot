package main

import (
	"fmt"
	"log"

	"github.com/ad/cron"
	"github.com/boltdb/bolt"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
	"github.com/tevino/abool"
	"gopkg.in/telegram-bot-api.v4"
)

type telegramResponse struct {
	body string
	key  string
}

var (
	telegramResp chan telegramResponse

	state                    = make(map[string]int)
	editMessage              = make(map[string]map[int]int)
	likesToAccountPerSession = make(map[string]int)

	reportID      int64
	admins        []string
	telegramToken string
	instaUsername string
	instaPassword string

	commandKeyboard tgbotapi.ReplyKeyboardMarkup

	followIsStarted       = abool.New()
	unfollowIsStarted     = abool.New()
	refollowIsStarted     = abool.New()
	followLikersIsStarted = abool.New()
	followQueueIsStarted  = abool.New()

	cronFollow      int
	cronUnfollow    int
	cronStats       int
	cronLike        int
	cronFollowQueue int
)
var db *bolt.DB

func main() {
	editMessage["follow"] = make(map[int]int)
	editMessage["unfollow"] = make(map[int]int)
	editMessage["refollow"] = make(map[int]int)
	editMessage["followLikers"] = make(map[int]int)

	db, err := initBolt()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	c := cron.New()
	c.Start()
	defer c.Stop()

	go login()

	telegramResp = make(chan telegramResponse)

	startFollowChan, _, _, stopFollowChan := followManager(db)
	startUnfollowChan, _, _, stopUnfollowChan := unfollowManager(db)
	startRefollowChan, _, innerRefollowChan, stopRefollowChan := refollowManager(db)
	startfollowLikersChan, _, innerfollowLikersChan, stopFollowLikersChan := followLikersManager(db)

	bot, err := tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = false
	log.Printf("Authorized on account %s", bot.Self.UserName)

	var ucfg = tgbotapi.NewUpdate(0)
	ucfg.Timeout = 60

	updates, err := bot.GetUpdatesChan(ucfg)

	if err != nil {
		log.Fatalf("[INIT] [Failed to init Telegram updates chan: %v]", err)
	}

	cronFollow, _ = c.AddFunc("0 0 9 * * *", func() { fmt.Println("Start follow"); startFollow(bot, startFollowChan, reportID) })
	cronUnfollow, _ = c.AddFunc("0 0 22 * * *", func() { fmt.Println("Start unfollow"); startUnfollow(bot, startUnfollowChan, reportID) })
	cronStats, _ = c.AddFunc("0 59 23 * * *", func() { fmt.Println("Send stats"); sendStats(bot, db, c, -1) })
	cronLike, _ = c.AddFunc("0 30 10-21 * * *", func() { fmt.Println("Like followers"); likeFollowersPosts(db) })

	for _, task := range c.Entries() {
		log.Println(task.Next)
	}

	// read updated
	for { //update := range updates {
		select {
		case update := <-updates:
			if update.EditedMessage != nil {
				continue
			}

			// UserName := update.Message.From.UserName
			// log.Println(UserID)
			if intInStringSlice(int(update.Message.From.ID), admins) {
				// ChatID := update.Message.Chat.ID

				text := update.Message.Text
				command := update.Message.Command()
				args := update.Message.CommandArguments()

				// log.Printf("[%d] %s, %s, %s", UserID, Text, Command, Args)

				msg := tgbotapi.NewMessage(int64(update.Message.From.ID), "")
				msg.DisableWebPagePreview = true
				msg.DisableNotification = true

				switch command {

				case "refollow":
					if args == "" {
						msg.Text = fmt.Sprintf("/refollow username")
						bot.Send(msg)
					} else {
						startRefollow(bot, startRefollowChan, innerRefollowChan, int64(update.Message.From.ID), args)
					}
				case "followlikers":
					if args == "" {
						msg.Text = fmt.Sprintf("/followlikers post link")
						bot.Send(msg)
					} else {
						startFollowLikers(bot, startfollowLikersChan, innerfollowLikersChan, int64(update.Message.From.ID), args)
					}
				case "follow":
					startFollow(bot, startFollowChan, int64(update.Message.From.ID))
				case "unfollow":
					startUnfollow(bot, startUnfollowChan, int64(update.Message.From.ID))
				case "progress":
					var unfollowProgress = "not started"
					if state["unfollow"] >= 0 {
						unfollowProgress = fmt.Sprintf("%d%% [%d/%d]", state["unfollow"], state["unfollow_current"], state["unfollow_all_count"])
					}
					var followProgress = "not started"
					if state["follow"] >= 0 {
						followProgress = fmt.Sprintf("%d%% [%d/%d]", state["follow"], state["follow_current"], state["follow_all_count"])
					}
					var refollowProgress = "not started"
					if state["refollow"] >= 0 {
						refollowProgress = fmt.Sprintf("%d%% [%d/%d]", state["refollow"], state["refollow_current"], state["refollow_all_count"])
					}
					var followLikersProgress = "not started"
					if state["followLikers"] >= 0 {
						followLikersProgress = fmt.Sprintf("%d%% [%d/%d]", state["followLikers"], state["followLikers_current"], state["followLikers_all_count"])
					}
					msg.Text = fmt.Sprintf("Unfollow — %s\nFollow — %s\nRefollow — %s\nfollowLikers - %s", unfollowProgress, followProgress, refollowProgress, followLikersProgress)
					msgRes, err := bot.Send(msg)
					if err != nil {
						editMessage["progress"][update.Message.From.ID] = msgRes.MessageID
					}
				case "cancelfollow":
					if followIsStarted.IsSet() {
						stopFollowChan <- true
					}
				case "cancelunfollow":
					if unfollowIsStarted.IsSet() {
						stopUnfollowChan <- true
					}
				case "cancelrefollow":
					if refollowIsStarted.IsSet() {
						stopRefollowChan <- true
					}
				case "cancelfollowlikers":
					if followLikersIsStarted.IsSet() {
						stopFollowLikersChan <- true
					}
				case "stats":
					sendStats(bot, db, c, int64(update.Message.From.ID))
				case "getcomments":
					sendComments(bot, int64(update.Message.From.ID))
				case "addcomments":
					addComments(bot, args, int64(update.Message.From.ID))
				case "removecomments":
					removeComments(bot, args, int64(update.Message.From.ID))
				case "gettags":
					sendTags(bot, int64(update.Message.From.ID))
				case "addtags":
					addTags(bot, args, int64(update.Message.From.ID))
				case "removetags":
					removeTags(bot, args, int64(update.Message.From.ID))
				case "getlimits":
					getLimits(bot, int64(update.Message.From.ID))
				case "updatelimits":
					updateLimits(bot, args, int64(update.Message.From.ID))
				case "like":
					likeFollowersPosts(db)
				case "watch":
					if args == "" {
						msg.Text = fmt.Sprintf("/watch username")
						bot.Send(msg)
					} else {
						addWatching(bot, db, args, int64(update.Message.From.ID))
					}

				case "watching":
					sendWatching(bot, db, int64(update.Message.From.ID))

				case "startfollowqueue":
					startFollowFromQueue(db, 100)
					//getUsersFromQueue(db, 1)
					//iterateDB(db, []byte("followqueue"))
				case "queuesize":
					sendQueueSize(bot, db, int64(update.Message.From.ID), "followqueue")
				case "scrap":
					watchinguser, _ := getWatchingUser(db)
					scrapFollowersFromUser(db, watchinguser)

				default:
					msg.Text = text
					msg.ReplyMarkup = commandKeyboard
					bot.Send(msg)
				}
			}
		case resp := <-telegramResp:
			log.Println(resp.key, resp.body)
			if resp.key != "" {
				if len(editMessage[resp.key]) > 0 {
					for UserID, EditID := range editMessage[resp.key] {
						edit := tgbotapi.EditMessageTextConfig{
							BaseEdit: tgbotapi.BaseEdit{
								ChatID:    int64(UserID),
								MessageID: EditID,
							},
							Text: resp.body,
						}
						bot.Send(edit)
					}
				} else {
					msg := tgbotapi.NewMessage(reportID, resp.body)
					msgRes, err := bot.Send(msg)
					if err == nil {
						editMessage[resp.key][int(reportID)] = msgRes.MessageID
					}
				}
			}
		}
	}
}

func init() {
	initKeyboard()
	parseOptions()
	getConfig()

	viper.WatchConfig()
	viper.OnConfigChange(func(e fsnotify.Event) {
		fmt.Println("Config file changed:", e.Name)
		getConfig()
	})
}

func initKeyboard() {
	commandKeyboard = tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("/stats"),
			tgbotapi.NewKeyboardButton("/progress"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("/follow"),
			tgbotapi.NewKeyboardButton("/unfollow"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("/cancelfollow"),
			tgbotapi.NewKeyboardButton("/cancelunfollow"),
			tgbotapi.NewKeyboardButton("/cancelrefollow"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("/getcomments"),
			tgbotapi.NewKeyboardButton("/gettags"),
		),
	)
}
