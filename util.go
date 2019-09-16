package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/ahmdrz/goinsta/v2"

	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

// Whether we are in development mode or not
var dev *bool
var configFile *string

// An image will be liked if the poster has more followers than likeLowerLimit, and less than likeUpperLimit
var likeLowerLimit int
var likeUpperLimit int
var likeCount int

// A user will be followed if he has more followers than followLowerLimit, and less than followUpperLimit
// Needs to be a subset of the like interval
// var followLowerLimit int
// var followUpperLimit int
var followCount int
var potencyRatio float64

// An image will be commented if the poster has more followers than commentLowerLimit, and less than commentUpperLimit
// Needs to be a subset of the like interval
var commentLowerLimit int
var commentUpperLimit int
var commentCount int

var maxLikesToAccountPerSession int

// Hashtags list. Do not put the '#' in the config file
var tagsList []string

// Limits for the current hashtag
var limits map[string]int

// Comments list
var commentsList []string

// White list. Do not put the '@' in the config file
var whiteList []string

// Report that will be sent at the end of the script
var report map[string]map[string]int

// Counters that will be incremented while we like, comment, and follow
var numFollowed int
var numLiked int
var numCommented int

// check will log.Fatal if err is an error
func check(err error) {
	if err != nil {
		log.Println("ERROR:", err)
	}
}

// Parses the options given to the script
func parseOptions() {
	dev = flag.Bool("dev", false, "Use this option to use the script in development mode : nothing will be done for real")
	configFile = flag.String("config", "config/config.json", "Path to config file")
	logs := flag.Bool("logs", false, "Use this option to enable the logfile")

	flag.Parse()

	// -logs enables the log file
	if *logs {
		// Opens a log file
		t := time.Now()
		logFile, err := os.OpenFile("instabot-"+t.Format("2006-01-02-15-04-05")+".log", os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)
		check(err)
		defer logFile.Close()

		// Duplicates the writer to stdout and logFile
		mw := io.MultiWriter(os.Stdout, logFile)
		log.SetOutput(mw)
	}
}

// Gets the conf in the config file
func getConfig() {
	viper.SetConfigFile(*configFile)

	// Reads the config file
	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file, %s", err)
	}

	// Confirms which config file is used
	log.Printf("Using config: %s\n\n", viper.ConfigFileUsed())

	likeLowerLimit = viper.GetInt("limits.like.min")
	likeUpperLimit = viper.GetInt("limits.like.max")
	likeCount = viper.GetInt("limits.like.count")

	// followLowerLimit = viper.GetInt("limits.follow.min")
	// followUpperLimit = viper.GetInt("limits.follow.max")
	followCount = viper.GetInt("limits.follow.count")
	potencyRatio = viper.GetFloat64("limits.follow.potency_ratio")

	commentLowerLimit = viper.GetInt("limits.comment.min")
	commentUpperLimit = viper.GetInt("limits.comment.max")
	commentCount = viper.GetInt("limits.comment.count")

	viper.SetDefault("limits.max_likes_to_account_per_session", 10)
	maxLikesToAccountPerSession = viper.GetInt("limits.max_likes_to_account_per_session")

	tagsList = viper.GetStringSlice("tags")

	commentsList = viper.GetStringSlice("comments")

	whiteList = viper.GetStringSlice("whitelist")

	reportID = viper.GetInt64("user.telegram.reportID")
	admins = viper.GetStringSlice("user.telegram.admins")

	telegramToken = viper.GetString("user.telegram.token")
	telegramProxy = viper.GetString("user.telegram.proxy")
	telegramProxyPort = viper.GetInt32("user.telegram.proxy_port")
	telegramProxyUser = viper.GetString("user.telegram.proxy_user")
	telegramProxyPassword = viper.GetString("user.telegram.proxy_password")

	instaUsername = viper.GetString("user.instagram.username")
	instaPassword = viper.GetString("user.instagram.password")
	instaProxy = viper.GetString("user.instagram.proxy")

	report = make(map[string]map[string]int)
}

// Sends an telegram. Check out the "telegram" section of the "config.json" file.
func send(body string, success bool) {
	bot, err := tgbotapi.NewBotAPI(viper.GetString("user.telegram.token"))
	if err != nil {
		log.Println(err)
		return
	}

	bot.Debug = false
	log.Printf("Authorized on account %s", bot.Self.UserName)

	msg := tgbotapi.NewMessage(viper.GetInt64("user.telegram.id"), body)
	bot.Send(msg)
}

// Retries the same function [function], a certain number of times (maxAttempts).
// It is exponential : the 1st time it will be (sleep), the 2nd time, (sleep) x 2, the 3rd time, (sleep) x 3, etc.
// If this function fails to recover after an error, it will send an email to the address in the config file.
func retry(maxAttempts int, sleep time.Duration, function func() error) (err error) {
	for currentAttempt := 0; currentAttempt < maxAttempts; currentAttempt++ {
		err = function()
		if err == nil {
			return
		}
		for i := 0; i <= currentAttempt; i++ {
			time.Sleep(sleep)
		}
		log.Println("Retrying after error:", err)
	}

	send(fmt.Sprintf("The script has stopped due to an unrecoverable error :\n%s", err), false)
	return fmt.Errorf("After %d attempts, last error: %s", maxAttempts, err)
}

func shuffle(slice interface{}) {
	rv := reflect.ValueOf(slice)
	swap := reflect.Swapper(slice)
	length := rv.Len()
	for i := length - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		swap(i, j)
	}
}

func getKeys(slice interface{}) []string {
	keys := reflect.ValueOf(slice).MapKeys()
	// log.Println(keys)
	strkeys := make([]string, len(keys))
	for i := 0; i < len(keys); i++ {
		strkeys[i] = keys[i].String()
	}

	return strkeys
}

func intInStringSlice(a int, list []string) bool {
	b := strconv.Itoa(a)
	for _, c := range list {
		if b == c {
			return true
		}
	}
	return false
}

func stringInStringSlice(a string, list []string) bool {
	for _, b := range list {
		if a == b {
			return true
		}
	}
	return false
}

func sliceUnique(input []string) []string {
	u := make([]string, 0, len(input))
	m := make(map[string]bool)

	for _, val := range input {
		if _, ok := m[val]; !ok {
			m[val] = true
			u = append(u, val)
		}
	}

	return u
}

func getInput(text string) string {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf(text)

	input, err := reader.ReadString('\n')
	check(err)
	return strings.TrimSpace(input)
}

// Checks if the user is in the slice
func contains(slice []goinsta.User, user goinsta.User) bool {
	for index := range slice {
		if user.ID == slice[index].ID {
			return true
		}
	}
	return false
}
