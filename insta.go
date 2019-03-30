package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ad/cron"

	"github.com/tducasse/goinsta"
	"github.com/tducasse/goinsta/response"
	"github.com/tducasse/goinsta/store"

	"github.com/boltdb/bolt"
	"github.com/spf13/viper"

	tgbotapi "gopkg.in/telegram-bot-api.v4"
)

// Insta is a goinsta.Instagram instance
var insta *goinsta.Instagram

var usersInfo = make(map[string]response.GetUsernameResponse)
var tagFeed = make(map[string]response.TagFeedsResponse)

var reportAsString string

// login will try to reload a previous session, and will create a new one if it can't
func login() {
	err := reloadSession()
	if err != nil {
		createAndSaveSession()
	}
}

func refollowManager(db *bolt.DB) (startChan chan bool, outerChan, innerChan chan string, stopChan chan bool) {
	startChan = make(chan bool)
	outerChan = make(chan string)
	innerChan = make(chan string)
	stopChan = make(chan bool)
	go func() {
		for {
			select {
			case <-startChan:
				if !refollowIsStarted.IsSet() {
					refollowIsStarted.Set()
					go followFollowers(db, innerChan, stopChan)
					// innerChan <- "start"
				} else {
					fmt.Println("can't start task, task already running!")
				}
			case msg := <-outerChan:
				fmt.Println("refollow <- ", msg)
				//default:
				//	time.Sleep(100 * time.Millisecond)
			}
		}
	}()
	return startChan, outerChan, innerChan, stopChan
}

func followFollowers(db *bolt.DB, innerChan chan string, stopChan chan bool) {
	defer refollowIsStarted.UnSet()
	for {
		select {
		case msg := <-innerChan:
			fmt.Println("refollow <- ", msg)
			go func() {
				l.Lock()
				state["refollow"] = 0
				l.Unlock()

				time.Sleep(1 * time.Second)
				username := msg
				user, err := insta.GetUserByUsername(username)
				if err != nil {
					telegramResp <- telegramResponse{fmt.Sprintf("%s", err), "refollow"}
					stopChan <- true
					return
				}

				if user.User.IsPrivate {
					userFriendShip, err := insta.UserFriendShip(user.User.ID)
					check(err)
					if !userFriendShip.Following {
						telegramResp <- telegramResponse{"User profile is private and we are not following, can't process", "refollow"}
						stopChan <- true
						return
					}
				}

				followers, err := insta.TotalUserFollowers(user.User.ID)
				//log.Println(followers)
				check(err)
				var users = followers.Users
				if len(users) > 0 {
					rand.Seed(time.Now().UnixNano()) // do it once during app initialization
					shuffle(users)
				}

				var limit = viper.GetInt("limits.maxSync")
				if limit <= 0 || limit >= 1000 {
					limit = 1000
				}

				today, _ := getStats(db, "refollow")
				if today > 0 {
					limit = limit - today
				}

				var allCount = int(math.Min(float64(len(users)), float64(limit)))
				switch {
				case allCount == 0 && len(users) > 0:
					telegramResp <- telegramResponse{"Follow limit reached :(", "refollow"}
				case allCount <= 0:
					telegramResp <- telegramResponse{"Followers not found :(", "refollow"}
				default:
					var current = 0

					telegramResp <- telegramResponse{fmt.Sprintf("%d users will be followed", allCount), "refollow"}

					for index := range users {
						if !refollowIsStarted.IsSet() {
							stopChan <- true
							return
						}
						if current >= limit {
							continue
						}

						if users[index].IsPrivate {
							log.Printf("%s is private, skipping\n", users[index].Username)
						} else {
							previoslyFollowed, _ := getFollowed(db, users[index].Username)
							if previoslyFollowed != "" {
								log.Printf("%s previously followed at %s, skipping\n", users[index].Username, previoslyFollowed)
							} else {
								current++

								l.Lock()
								state["refollow"] = int(current * 100 / allCount)
								state["refollow_current"] = current
								state["refollow_all_count"] = allCount
								l.Unlock()

								text := fmt.Sprintf("[%d/%d] refollowing %s (%d%%)", state["refollow_current"], state["refollow_all_count"], users[index].Username, state["refollow"])
								telegramResp <- telegramResponse{text, "refollow"}

								if !*dev {
									insta.Follow(users[index].ID)
									setFollowed(db, users[index].Username)
									incStats(db, "follow")
									incStats(db, "refollow")
									time.Sleep(16 * time.Second)
								} else {
									time.Sleep(2 * time.Second)
								}
							}
						}
					}
				}
				stopChan <- true
			}()
		case <-stopChan:
			telegramResp <- telegramResponse{fmt.Sprintf("\nRefollowed %d users!\n", state["refollow_current"]), "refollow"}
			l.Lock()
			editMessage["refollow"] = make(map[int]int)
			state["refollow"] = -1
			l.Unlock()
			return
			//default:
			//	time.Sleep(100 * time.Millisecond)
		}
	}
}

func followLikersManager(db *bolt.DB) (startChan chan bool, outerChan, innerChan chan string, stopChan chan bool) {
	startChan = make(chan bool)
	outerChan = make(chan string)
	innerChan = make(chan string)
	stopChan = make(chan bool)
	go func() {
		for {
			select {
			case <-startChan:
				if !followLikersIsStarted.IsSet() {
					followLikersIsStarted.Set()
					go followLikers(db, innerChan, stopChan)
					// innerChan <- "start"
				} else {
					fmt.Println("can't start task, task already running!")
				}
			case msg := <-outerChan:
				fmt.Println("followLikers <- ", msg)
				//default:
				//	time.Sleep(100 * time.Millisecond)
			}
		}
	}()
	return startChan, outerChan, innerChan, stopChan
}

func followLikers(db *bolt.DB, innerChan chan string, stopChan chan bool) {
	defer followLikersIsStarted.UnSet()
	for {
		select {
		case msg := <-innerChan:
			fmt.Println("followLikers <- ", msg)
			go func() {
				l.Lock()
				state["followLikers"] = 0
				l.Unlock()

				time.Sleep(1 * time.Second)

				if len(msg) > 0 {
					u, err := url.Parse(msg)
					if err == nil {
						test := strings.TrimPrefix(u.Path, "/p/")
						test = strings.TrimSuffix(test, "/")
						likers, err := insta.MediaLikers(mediaFromCode(test))
						if err != nil {
							println(err.Error())
						} else {
							users := likers.Users
							if len(users) > 0 {
								println(fmt.Sprintf("found %d likers", len(users)))
								rand.Seed(time.Now().UnixNano()) // do it once during app initialization
								shuffle(users)

								var limit = viper.GetInt("limits.maxSync")
								if limit <= 0 || limit >= 1000 {
									limit = 1000
								}

								today, _ := getStats(db, "followLikers")
								if today > 0 {
									limit = limit - today
								}

								var allCount = int(math.Min(float64(len(users)), float64(limit)))
								switch {
								case allCount == 0 && len(users) > 0:
									telegramResp <- telegramResponse{"Follow limit reached :(", "followLikers"}
								case allCount <= 0:
									telegramResp <- telegramResponse{"Likers not found :(", "followLikers"}
								default:
									var current = 0

									telegramResp <- telegramResponse{fmt.Sprintf("%d users will be followed", allCount), "followLikers"}

									for index := range users {
										if !followLikersIsStarted.IsSet() {
											stopChan <- true
											return
										}
										if current >= limit {
											continue
										}

										if users[index].IsPrivate {
											log.Printf("%s is private, skipping\n", users[index].Username)
										} else {
											previoslyFollowed, _ := getFollowed(db, users[index].Username)
											if previoslyFollowed != "" {
												log.Printf("%s previously followed at %s, skipping\n", users[index].Username, previoslyFollowed)
											} else {
												current++

												l.Lock()
												state["followLikers"] = int(current * 100 / allCount)
												state["followLikers_current"] = current
												state["followLikers_all_count"] = allCount
												l.Unlock()

												text := fmt.Sprintf("[%d/%d] following %s (%d%%)", state["followLikers_current"], state["followLikers_all_count"], users[index].Username, state["followLikers"])
												telegramResp <- telegramResponse{text, "followLikers"}

												if !*dev {
													insta.Follow(users[index].ID)
													setFollowed(db, users[index].Username)
													incStats(db, "follow")
													incStats(db, "followLikers")
													time.Sleep(16 * time.Second)
												} else {
													time.Sleep(2 * time.Second)
												}
											}
										}
									}
								}
							} else {
								println("likers not found")
							}
						}
					}
				}
				stopChan <- true
			}()
		case <-stopChan:
			telegramResp <- telegramResponse{fmt.Sprintf("\nfollowed %d users!\n", state["followLikers_current"]), "followLikers"}
			l.Lock()
			editMessage["followLikers"] = make(map[int]int)
			state["followLikers"] = -1
			l.Unlock()
			return
			//default:
			//	time.Sleep(100 * time.Millisecond)
		}
	}
}

func unfollowManager(db *bolt.DB) (startChan chan bool, outerChan, innerChan chan string, stopChan chan bool) {
	startChan = make(chan bool)
	outerChan = make(chan string)
	innerChan = make(chan string)
	stopChan = make(chan bool)
	go func() {
		for {
			select {
			case <-startChan:
				if !unfollowIsStarted.IsSet() {
					unfollowIsStarted.Set()
					go syncFollowers(db, innerChan, stopChan)
					innerChan <- "start"
				} else {
					fmt.Println("can't start task, task already running!")
				}
			case msg := <-outerChan:
				fmt.Println("unfollow <- ", msg)
				//default:
				//	time.Sleep(100 * time.Millisecond)
			}
		}
	}()
	return startChan, outerChan, innerChan, stopChan
}

func syncFollowers(db *bolt.DB, innerChan chan string, stopChan chan bool) {
	defer unfollowIsStarted.UnSet()
	for {
		select {
		case msg := <-innerChan:
			fmt.Println("unfollow <- ", msg)
			go func() {

				l.Lock()
				state["unfollow"] = 0
				l.Unlock()

				time.Sleep(1 * time.Second)

				var limit = viper.GetInt("limits.max_unfollow_per_day")
				today, _ := getStats(db, "unfollow")

				if limit == 0 || (limit-today) <= 0 {
					stopChan <- true
					return
				}

				following, _ := insta.SelfTotalUserFollowing()
				// check(err)
				followers, _ := insta.SelfTotalUserFollowers()
				// check(err)

				var daysBeforeUnfollow = viper.GetInt("limits.days_before_unfollow")
				if daysBeforeUnfollow <= 0 || daysBeforeUnfollow >= 30 {
					daysBeforeUnfollow = 3
				}

				var users []response.User
				for index := range following.Users {
					if !contains(followers.Users, following.Users[index]) {
						previoslyFollowed, _ := getFollowed(db, following.Users[index].Username)
						if previoslyFollowed != "" {
							t, err := time.Parse("20060102", previoslyFollowed)

							if err != nil {
								fmt.Println(err)
							} else {
								duration := time.Since(t)
								if int(duration.Hours()) < (24 * daysBeforeUnfollow) {
									fmt.Printf("%s not followed us less then %f hours, skipping!\n", following.Users[index].Username, duration.Hours())
									continue
								} else {
									users = append(users, following.Users[index])
								}
							}
						} else {
							users = append(users, following.Users[index])
						}
					}
				}

				lastLikers := getLastLikers()
				if len(lastLikers) > 0 {
					if len(following.Users) > 0 {
						telegramResp <- telegramResponse{fmt.Sprintf("Found %d following, %d likers for last 10 posts\n", len(following.Users), len(lastLikers)), "unfollow"}
						var notLikers []response.User
						for index := range following.Users {
							if !stringInStringSlice(following.Users[index].Username, lastLikers) {
								notLikers = append(notLikers, following.Users[index])
							}
						}

						if len(notLikers) > 0 {
							for index := range notLikers {
								previoslyFollowed, _ := getFollowed(db, notLikers[index].Username)
								if previoslyFollowed != "" {
									t, err := time.Parse("20060102", previoslyFollowed)

									if err != nil {
										fmt.Println(err)
									} else {
										duration := time.Since(t)
										if int(duration.Hours()) < (24 * daysBeforeUnfollow) {

										} else {
											if !contains(users, notLikers[index]) {
												users = append(users, notLikers[index])
											}
										}
									}
								} else {
									if !contains(users, notLikers[index]) {
										users = append(users, notLikers[index])
									}
								}
							}
						}
					}
				}

				if limit <= 0 {
					limit = 1000
				}

				if today > 0 {
					limit = limit - today
				}

				var current = 0
				var allCount = int(math.Min(float64(len(users)), float64(limit)))
				if allCount > 0 {
					telegramResp <- telegramResponse{fmt.Sprintf("%d will be unfollowed", allCount), "unfollow"}

					for index := range users {
						if !unfollowIsStarted.IsSet() {
							stopChan <- true
							return
						}

						if current >= limit {
							continue
						}

						if stringInStringSlice(users[index].Username, whiteList) {
							telegramResp <- telegramResponse{fmt.Sprintf("[%d/%d] Skip Unfollowing %s (%d%%), in white list\n", state["unfollow_current"], state["unfollow_all_count"], users[index].Username, state["unfollow"]), "unfollow"}
							continue
						}

						current++
						l.Lock()
						state["unfollow"] = int(current * 100 / allCount)
						state["unfollow_current"] = current
						state["unfollow_all_count"] = allCount
						l.Unlock()

						telegramResp <- telegramResponse{fmt.Sprintf("[%d/%d] Unfollowing %s (%d%%)\n", state["unfollow_current"], state["unfollow_all_count"], users[index].Username, state["unfollow"]), "unfollow"}
						if !*dev {
							insta.UnFollow(users[index].ID)
							setFollowed(db, users[index].Username)
							incStats(db, "unfollow")
							time.Sleep(16 * time.Second)
						} else {
							time.Sleep(2 * time.Second)
						}
					}
				}

				stopChan <- true
			}()
		case <-stopChan:
			if state["unfollow_current"] == 0 {
				telegramResp <- telegramResponse{fmt.Sprintf("No one unfollow"), "unfollow"}
			} else {
				telegramResp <- telegramResponse{fmt.Sprintf("\nUnfollowed %d users are not following you back!\n", state["unfollow_current"]), "unfollow"}
			}

			l.Lock()
			editMessage["unfollow"] = make(map[int]int)
			state["unfollow_current"] = 0
			state["unfollow_all_count"] = 0
			state["unfollow"] = -1
			l.Unlock()

			return
			//default:
			//	time.Sleep(100 * time.Millisecond)
		}
	}
}

// Logins and saves the session
func createAndSaveSession() {
	insta = goinsta.New(instaUsername, instaPassword)
	err := insta.Login()
	check(err)

	key := createKey()
	bytes, err := store.Export(insta, key)
	check(err)
	err = ioutil.WriteFile("session", bytes, 0644)
	check(err)
	log.Println("Created and saved the session")
}

// reloadSession will attempt to recover a previous session
func reloadSession() error {
	if _, err := os.Stat("session"); os.IsNotExist(err) {
		return errors.New("No session found")
	}

	session, err := ioutil.ReadFile("session")
	check(err)
	log.Println("A session file exists")

	key, err := ioutil.ReadFile("key")
	check(err)

	insta, err = store.Import(session, key)
	if err != nil {
		return errors.New("Couldn't recover the session")
	}

	log.Println("Successfully logged in")
	return nil

}

// createKey creates a key and saves it to file
func createKey() []byte {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	check(err)
	err = ioutil.WriteFile("key", key, 0644)
	check(err)
	log.Println("Created and saved the key")
	return key
}

func followManager(db *bolt.DB) (startChan chan bool, outerChan, innerChan chan string, stopChan chan bool) {
	startChan = make(chan bool)
	outerChan = make(chan string)
	innerChan = make(chan string)
	stopChan = make(chan bool)
	go func() {
		for {
			select {
			case <-startChan:
				if !followIsStarted.IsSet() {
					followIsStarted.Set()
					go loopTags(db, innerChan, stopChan)
					innerChan <- "start"
				} else {
					fmt.Println("can't start task, task already running!")
				}
			case msg := <-outerChan:
				fmt.Println("follow <- ", msg)
				//default:
				//	time.Sleep(100 * time.Millisecond)
			}
		}
	}()
	return startChan, outerChan, innerChan, stopChan
}

// Go through all the tags in the list
func loopTags(db *bolt.DB, innerChan chan string, stopChan chan bool) {
	usersInfo = make(map[string]response.GetUsernameResponse)
	tagFeed = make(map[string]response.TagFeedsResponse)

	followStartedAt := time.Now()

	defer followIsStarted.UnSet()
	for {
		select {
		case msg := <-innerChan:
			fmt.Println("follow <- ", msg)
			go func() {

				l.Lock()
				followStartedAt = time.Now()
				state["follow"] = 0
				reportAsString = ""
				l.Unlock()

				time.Sleep(1 * time.Second)

				report = make(map[string]map[string]int)
				likesToAccountPerSession = make(map[string]int)

				followTestUsername := viper.GetString("user.instagram.follow_test_username")
				if followTestUsername != "" {
					user, err := insta.GetUserByUsername(followTestUsername)
					if err != nil {
						log.Println("test instagram username not found")
					} else {
						_, err := insta.Follow(user.User.ID)
						if err != nil {
							text := fmt.Sprintf("test user not followed, cancel /follow %v", err)
							telegramResp <- telegramResponse{text, "follow"}

							stopChan <- true
							return
						}
						insta.UnFollow(user.User.ID)
					}
				}

				var allCount = len(tagsList)
				if allCount > 0 {
					var current = 0

					shuffle(tagsList)
					for _, tag := range tagsList {
						if !followIsStarted.IsSet() {
							stopChan <- true
							return
						}

						current++

						l.Lock()
						state["follow"] = int((current - 1) * 100 / allCount)
						state["follow_current"] = current
						state["follow_all_count"] = allCount
						l.Unlock()

						// limitsConf := viper.GetStringMap("tags." + tag)

						// // Some converting
						// limits = map[string]int{
						// 	"follow":  int(limitsConf["follow"].(float64)),
						// 	"like":    int(limitsConf["like"].(float64)),
						// 	"comment": int(limitsConf["comment"].(float64)),
						// }

						// What we did so far
						numFollowed = 0
						numLiked = 0
						numCommented = 0

						reportAsString = fmt.Sprintf("\n[%d/%d] ➜ Current tag is %s (%d%%)", state["follow_current"], state["follow_all_count"], tag, state["follow"])
						if current > 1 {
							l.RLock()
							elapsed := time.Since(followStartedAt)
							l.RUnlock()
							perOne := elapsed.Seconds() / float64(current)
							duration := time.Duration(time.Duration(perOne*float64(allCount-current+1)) * time.Second)
							reportAsString += fmt.Sprintf(" ~%s", duration.Round(time.Second))
						}

						for tag := range report {
							reportAsString += fmt.Sprintf("\n#%s: %d 🐾, %d 👍, %d 💌", tag, report[tag]["follow"], report[tag]["like"], report[tag]["comment"])
						}
						telegramResp <- telegramResponse{reportAsString, "follow"}
						browse(tag, db, stopChan)
						if current != allCount {
							time.Sleep(10 * time.Second)
						}
					}
				}

				stopChan <- true
			}()
		case <-stopChan:

			l.RLock()
			elapsed := time.Since(followStartedAt)
			l.RUnlock()

			if reportAsString != "" {
				reportAsString += fmt.Sprintf("\nFollowing is finished by %s\n", elapsed.Round(time.Second))
				telegramResp <- telegramResponse{reportAsString, "follow"}
			} else {
				telegramResp <- telegramResponse{"Follow finished", "follow"}
			}

			l.Lock()
			editMessage["follow"] = make(map[int]int)
			state["follow"] = -1
			reportAsString = ""
			l.Unlock()

			return
		}
	}
}

// Browses the page for a certain tag, until we reach the limits
func browse(tag string, db *bolt.DB, stopChan chan bool) {
	var i = 0
	for numFollowed < followCount || numLiked < likeCount || numCommented < commentCount {
		if !followIsStarted.IsSet() {
			stopChan <- true
			return
		}

		log.Println("Fetching the list of images for #" + tag)
		i++

		// Getting all the pictures we can on the first page
		// Instagram will return a 500 sometimes, so we will retry 10 times.
		// Check retry() for more info.

		var images, ok = tagFeed[tag]
		if ok {
			// log.Println("from cache #" + tag)
		} else {
			err := retry(10, 20*time.Second, func() (err error) {
				images, err = insta.TagFeed(tag)
				if err == nil {
					tagFeed[tag] = images
				}
				return
			})
			check(err)
		}

		goThrough(tag, db, images, stopChan)

		if viper.IsSet("limits.max_retry") && i > viper.GetInt("limits.max_retry") {
			log.Println("Currently not enough images for this tag to achieve goals")
			break
		}
	}
}

// Goes through all the images for a certain tag
func goThrough(tag string, db *bolt.DB, images response.TagFeedsResponse, stopChan chan bool) {
	var i = 1
	for index := range images.FeedsResponse.Items {
		if !followIsStarted.IsSet() {
			stopChan <- true
			return
		}
		// Exiting the loop if there is nothing left to do
		if numFollowed >= followCount && numLiked >= likeCount && numCommented >= commentCount {
			break
		}

		// Skip our own images
		if images.FeedsResponse.Items[index].User.Username == instaUsername {
			continue
		}

		// Check if we should fetch new images for tag
		if i >= followCount && i >= likeCount && i >= commentCount {
			break
		}

		if stringInStringSlice(images.FeedsResponse.Items[index].User.Username, whiteList) {
			log.Printf("Skip following %s, in white list\n", images.FeedsResponse.Items[index].User.Username)
			continue
		}

		// Getting the user info
		// Instagram will return a 500 sometimes, so we will retry 10 times.
		// Check retry() for more info.
		var posterInfo, ok = usersInfo[images.FeedsResponse.Items[index].User.Username]
		if ok {
			// log.Println("from cache " + posterInfo.User.Username + " - for #" + tag)
		} else {
			err := retry(10, 20*time.Second, func() (err error) {
				posterInfo, err = insta.GetUserByUsername(images.FeedsResponse.Items[index].User.Username)
				if err == nil {
					usersInfo[images.FeedsResponse.Items[index].User.Username] = posterInfo
				}
				return
			})
			check(err)
		}

		poster := posterInfo.User
		followerCount := poster.FollowerCount
		likesCount := images.FeedsResponse.Items[index].LikeCount
		commentsCount := images.FeedsResponse.Items[index].CommentCount
		followingCount := poster.FollowingCount

		// Will only follow and comment if we like the picture
		like := numLiked < likeCount && !images.FeedsResponse.Items[index].HasLiked
		follow := numFollowed < followCount && like
		comment := numCommented < commentCount && like

		var relationshipRatio float64 // = 0.0

		if followerCount != 0 && followingCount != 0 {
			relationshipRatio = float64(followingCount) / float64(followerCount)
		}

		// log.Println("Checking followers for " + poster.Username + " - for #" + tag)

		if follow {
			if relationshipRatio == 0 || relationshipRatio < potencyRatio {
				log.Printf("%s is not a potential user with the relationship ratio of %.2f (%d/%d) ~skipping user\n", poster.Username, relationshipRatio, followingCount, followerCount)
				follow = false
			} else {
				log.Printf("%s with the relationship ratio of %.2f (%d/%d)\n", poster.Username, relationshipRatio, followingCount, followerCount)
			}
		}

		// if followerCount > followUpperLimit {
		// 	log.Printf("%s has %d followers, more than max %d\n", poster.Username, followerCount, followUpperLimit)
		// 	follow = false
		// } else if followerCount < followLowerLimit {
		// 	log.Printf("%s has %d followers, less than min %d\n", poster.Username, followerCount, followLowerLimit)
		// 	follow = false
		// }

		if likesCount > likeUpperLimit {
			log.Printf("%s's image has %d likes, more than max %d\n", poster.Username, likesCount, likeUpperLimit)
			like = false
		} else if likesCount < likeLowerLimit {
			log.Printf("%s's image has %d likes, less than min %d\n", poster.Username, likesCount, likeLowerLimit)
			like = false
		}

		if commentsCount > commentUpperLimit {
			log.Printf("%s's image has %d comments, more than max %d\n", poster.Username, commentsCount, commentUpperLimit)
			comment = false
		} else if commentsCount < commentLowerLimit {
			log.Printf("%s's image has %d comments, less than min %d\n", poster.Username, commentsCount, commentLowerLimit)
			comment = false
		}

		if like || comment || follow {
			// log.Printf("%s has %d followers\n", poster.Username, followerCount)

			if relationshipRatio >= potencyRatio {
				i++
				// Like, then comment/follow
				if like {
					if userLikesCount, ok := likesToAccountPerSession[posterInfo.User.Username]; ok {
						if userLikesCount < maxLikesToAccountPerSession {
							likeImage(tag, db, images.FeedsResponse.Items[index], posterInfo)
							images.FeedsResponse.Items[index].HasLiked = true
						} else {
							log.Println("Likes count per user reached [" + poster.Username + "]")
						}
					} else {
						likeImage(tag, db, images.FeedsResponse.Items[index], posterInfo)
					}

					previoslyFollowed, _ := getFollowed(db, posterInfo.User.Username)
					if previoslyFollowed != "" {
						log.Printf("%s already following (%s), skipping\n", posterInfo.User.Username, previoslyFollowed)
					} else {
						if comment {
							if !images.FeedsResponse.Items[index].HasLiked {
								commentImage(tag, db, images.FeedsResponse.Items[index])
							}
						}
						if follow {
							followUser(tag, db, posterInfo)
						}
					}

					// This is to avoid the temporary ban by Instagram
					time.Sleep(30 * time.Second)
				}
			}
		} else {
			log.Printf("%s, nothing to do\n", poster.Username)
		}
		// log.Printf("%s done\n\n", poster.Username)
	}
}

// Likes an image, if not liked already
func likeImage(tag string, db *bolt.DB, image response.MediaItemResponse, userInfo response.GetUsernameResponse) {
	log.Println("Liking the picture https://www.instagram.com/p/" + image.Code)
	if !image.HasLiked {
		if !*dev {
			insta.Like(image.ID)
		}
		// log.Println("Liked")
		numLiked++

		if _, ok := report[tag]; !ok {
			report[tag] = make(map[string]int)
		}
		report[tag]["like"]++
		incStats(db, "like")
		likesToAccountPerSession[userInfo.User.Username]++
	} else {
		// log.Println("Image already liked")
	}
}

// Comments an image
func commentImage(tag string, db *bolt.DB, image response.MediaItemResponse) {
	rand.Seed(time.Now().Unix())
	text := commentsList[rand.Intn(len(commentsList))]
	if !*dev {
		insta.Comment(image.ID, text)
	}
	log.Println("Commented " + text)
	numCommented++

	if _, ok := report[tag]; !ok {
		report[tag] = make(map[string]int)
	}
	report[tag]["comment"]++
	incStats(db, "comment")
}

// Follows a user, if not following already
func followUser(tag string, db *bolt.DB, userInfo response.GetUsernameResponse) {
	user := userInfo.User

	userFriendShip, err := insta.UserFriendShip(user.ID)
	check(err)
	// If not following already
	if !userFriendShip.Following {

		if !*dev {
			if user.IsPrivate {
				log.Printf("%s is private, skipping follow\n", user.Username)
			} else {
				log.Printf("Following %s\n", user.Username)
				resp, err := insta.Follow(user.ID)
				if err != nil {
					log.Println(err)
				} else {
					userFriendShip.Following = resp.FriendShipStatus.Following

					if !userFriendShip.Following {
						log.Println("Not followed")
					}
				}
			}
		}
		// log.Println("Followed")
		if userFriendShip.Following {
			numFollowed++
			if _, ok := report[tag]; !ok {
				report[tag] = make(map[string]int)
			}
			report[tag]["follow"]++
			incStats(db, "follow")
			setFollowed(db, user.Username)
		}
	} else {
		log.Println("Already following " + user.Username)
	}
}

func startFollow(bot *tgbotapi.BotAPI, startChan chan bool, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	if followIsStarted.IsSet() {
		msg.Text = fmt.Sprintf("Follow in progress (%d%%)", state["follow"])

		l.RLock()
		rn := editMessage["follow"]
		ln := len(rn)
		l.RUnlock()

		if ln > 0 && intInStringSlice(int(userID), getKeys(rn)) {
			for UserID, EditID := range rn {
				edit := tgbotapi.EditMessageTextConfig{
					BaseEdit: tgbotapi.BaseEdit{
						ChatID:    int64(UserID),
						MessageID: EditID,
					},
					Text: msg.Text,
				}
				bot.Send(edit)
			}
		} else {
			msgRes, err := bot.Send(msg)
			if err == nil {
				l.Lock()
				editMessage["follow"][int(userID)] = msgRes.MessageID
				l.Unlock()
			}
		}
	} else {
		startChan <- true
		msg.Text = "Starting follow"
		msgRes, err := bot.Send(msg)
		if err == nil {
			l.Lock()
			editMessage["follow"][int(userID)] = msgRes.MessageID
			l.Unlock()
		}
	}
}

func startUnfollow(bot *tgbotapi.BotAPI, startChan chan bool, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	if unfollowIsStarted.IsSet() {
		msg.Text = fmt.Sprintf("Unfollow in progress (%d%%)", state["unfollow"])

		l.RLock()
		rn := editMessage["unfollow"]
		ln := len(rn)
		l.RUnlock()

		if ln > 0 && intInStringSlice(int(userID), getKeys(rn)) {
			for UserID, EditID := range rn {
				edit := tgbotapi.EditMessageTextConfig{
					BaseEdit: tgbotapi.BaseEdit{
						ChatID:    int64(UserID),
						MessageID: EditID,
					},
					Text: msg.Text,
				}
				bot.Send(edit)
			}
		} else {
			msgRes, err := bot.Send(msg)
			if err == nil {
				// log.Print(msgRes, msgRes.MessageID)
				l.Lock()
				editMessage["unfollow"][int(userID)] = msgRes.MessageID
				l.Unlock()
			}
		}
	} else {
		startChan <- true
		msg.Text = "Starting unfollow"
		fmt.Println(msg.Text)
		msgRes, err := bot.Send(msg)
		if err == nil {
			l.Lock()
			editMessage["unfollow"][int(userID)] = msgRes.MessageID
			l.Unlock()
		}
	}
}

func startRefollow(bot *tgbotapi.BotAPI, startChan chan bool, innerRefollowChan chan string, userID int64, target string) {
	msg := tgbotapi.NewMessage(userID, "")
	if refollowIsStarted.IsSet() {
		msg.Text = fmt.Sprintf("Refollow in progress (%d%%)", state["refollow"])

		l.RLock()
		rn := editMessage["refollow"]
		ln := len(rn)
		l.RUnlock()

		if ln > 0 && intInStringSlice(int(userID), getKeys(rn)) {
			edit := tgbotapi.EditMessageTextConfig{
				BaseEdit: tgbotapi.BaseEdit{
					ChatID:    int64(userID),
					MessageID: rn[int(userID)],
				},
				Text: msg.Text,
			}
			bot.Send(edit)
		} else {
			msgRes, err := bot.Send(msg)
			if err == nil {
				l.Lock()
				editMessage["refollow"][int(userID)] = msgRes.MessageID
				l.Unlock()
			}
		}
	} else {
		startChan <- true
		msg.Text = "Starting refollow"
		msgRes, err := bot.Send(msg)
		if err == nil {
			l.Lock()
			editMessage["refollow"][int(userID)] = msgRes.MessageID
			l.Unlock()
		}
		innerRefollowChan <- target
	}
}

func startFollowLikers(bot *tgbotapi.BotAPI, startChan chan bool, innerFollowLikersChan chan string, userID int64, target string) {
	msg := tgbotapi.NewMessage(userID, "")
	if followLikersIsStarted.IsSet() {
		msg.Text = fmt.Sprintf("followLikers in progress (%d%%)", state["followLikers"])

		l.RLock()
		rn := editMessage["followLikers"]
		ln := len(rn)
		l.RUnlock()

		if ln > 0 && intInStringSlice(int(userID), getKeys(rn)) {
			edit := tgbotapi.EditMessageTextConfig{
				BaseEdit: tgbotapi.BaseEdit{
					ChatID:    int64(userID),
					MessageID: rn[int(userID)],
				},
				Text: msg.Text,
			}
			bot.Send(edit)
		} else {
			msgRes, err := bot.Send(msg)
			if err == nil {
				l.Lock()
				editMessage["followLikers"][int(userID)] = msgRes.MessageID
				l.Unlock()
			}
		}
	} else {
		startChan <- true
		msg.Text = "Starting followLikers"
		msgRes, err := bot.Send(msg)
		if err == nil {
			l.Lock()
			editMessage["followLikers"][int(userID)] = msgRes.MessageID
			l.Unlock()
		}
		innerFollowLikersChan <- target
	}
}

func getJobState(c *cron.Cron, id int) (result string) {
	if id <= 0 {
		return "not found"
	}

	switch c.Status(id) {
	case 0:
		return "active"
	case 1:
		return "paused"
	case -1:
		return "not started"
	}
	return "unknown"
}

func sendStats(bot *tgbotapi.BotAPI, db *bolt.DB, c *cron.Cron, userID int64) {
	message := `<i>%s</i>

<b>Today stats</b>
Unfollowed — %d
Followed — %d
	Refollowed — %d
	Followed likers — %d
Liked — %d
Commented — %d

<b>Cron status</b>
follow %s
unfollow %s
like %s
stats %s`

	unfollowCount, _ := getStats(db, "unfollow")
	followCount, _ := getStats(db, "follow")
	refollowCount, _ := getStats(db, "refollow")
	followLikersCount, _ := getStats(db, "followLikers")
	likeCount, _ := getStats(db, "like")
	commentCount, _ := getStats(db, "comment")

	stats := getStatus()

	msg := tgbotapi.NewMessage(userID, fmt.Sprintf(message,
		stats,
		unfollowCount,
		followCount,
		refollowCount,
		followLikersCount,
		likeCount,
		commentCount,
		getJobState(c, cronFollow),
		getJobState(c, cronUnfollow),
		getJobState(c, cronStats),
		getJobState(c, cronLike),
	))

	msg.DisableWebPagePreview = true
	msg.ParseMode = "HTML"
	msg.DisableNotification = true

	if userID == -1 {
		for _, id := range admins {
			userID, _ = strconv.ParseInt(id, 10, 64)
			msg.ChatID = userID
			bot.Send(msg)
		}
	} else {
		bot.Send(msg)
	}
}

func sendComments(bot *tgbotapi.BotAPI, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	if len(commentsList) > 0 {
		msg.Text = strings.Join(commentsList, ", ")
	} else {
		msg.Text = "Comments is empty"
	}

	bot.Send(msg)
}

func addComments(bot *tgbotapi.BotAPI, comments string, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	if len(comments) > 0 {
		newComments := strings.Split(comments, ", ")
		newComments = append(commentsList, newComments...)
		newComments = sliceUnique(newComments)
		viper.Set("comments", newComments)
		viper.WriteConfig()
		msg.Text = "Comments updated"
	} else {
		msg.Text = "Comments is empty"
	}

	bot.Send(msg)
}

func removeComments(bot *tgbotapi.BotAPI, comments string, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	if len(comments) > 0 {
		removeComments := strings.Split(comments, ", ")
		var newComments []string
		for _, comment := range commentsList {
			if stringInStringSlice(comment, removeComments) {

			} else {
				newComments = append(newComments, comment)
			}
		}
		newComments = sliceUnique(newComments)
		viper.Set("comments", newComments)
		viper.WriteConfig()
		msg.Text = "Comments removed"
	} else {
		msg.Text = "Comments is empty"
	}

	bot.Send(msg)
}

func sendTags(bot *tgbotapi.BotAPI, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	if len(tagsList) > 0 {
		// keys := GetKeys(tagsList)
		// msg.Text = strings.Join(keys, ", ")
		msg.Text = strings.Join(tagsList, ", ")
	} else {
		msg.Text = "Tags is empty"
	}

	bot.Send(msg)
}

func addTags(bot *tgbotapi.BotAPI, tag string, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	// if len(tags) > 0 {
	tag = strings.Replace(tag, ".", "", -1)
	if len(tag) > 0 {
		newTags := strings.Split(tag, ", ")
		newTags = append(tagsList, newTags...)
		newTags = sliceUnique(newTags)
		viper.Set("tags", newTags)
		viper.WriteConfig()
		msg.Text = "Tags added"
	} else {
		msg.Text = "Tag is empty"
	}

	bot.Send(msg)
}

func removeTags(bot *tgbotapi.BotAPI, tags string, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	if len(tags) > 0 {
		removeTags := strings.Split(tags, ", ")
		var newTags []string
		for _, tag := range tagsList {
			if stringInStringSlice(tag, removeTags) {

			} else {
				newTags = append(newTags, tag)
			}
		}
		newTags = sliceUnique(newTags)
		viper.Set("tags", newTags)
		viper.WriteConfig()
		msg.Text = "Tags removed"
	} else {
		msg.Text = "Tags is empty"
	}

	bot.Send(msg)
}

func sendWhitelist(bot *tgbotapi.BotAPI, UserID int64) {
	msg := tgbotapi.NewMessage(UserID, "")
	if len(whiteList) > 0 {
		// keys := GetKeys(tagsList)
		// msg.Text = strings.Join(keys, ", ")
		msg.Text = strings.Join(whiteList, ", ")
	} else {
		msg.Text = "whitelist is empty"
	}

	bot.Send(msg)
}

func addWhitelist(bot *tgbotapi.BotAPI, item string, UserID int64) {
	msg := tgbotapi.NewMessage(UserID, "")
	// if len(tags) > 0 {
	item = strings.Replace(item, ".", "", -1)
	if len(item) > 0 {
		newWhiteList := strings.Split(item, ", ")
		newWhiteList = append(whiteList, newWhiteList...)
		newWhiteList = sliceUnique(newWhiteList)
		viper.Set("whitelist", newWhiteList)
		viper.WriteConfig()
		msg.Text = "whiteList added"
	} else {
		msg.Text = "Item is empty"
	}

	bot.Send(msg)
}

func removeWhitelist(bot *tgbotapi.BotAPI, items string, UserID int64) {
	msg := tgbotapi.NewMessage(UserID, "")
	if len(items) > 0 {
		removeWhiteList := strings.Split(items, ", ")
		var newWhiteList []string
		for _, item := range whiteList {
			if stringInStringSlice(item, removeWhiteList) {

			} else {
				newWhiteList = append(newWhiteList, item)
			}
		}
		newWhiteList = sliceUnique(newWhiteList)
		viper.Set("whitelist", newWhiteList)
		viper.WriteConfig()
		msg.Text = "whiteList removed"
	} else {
		msg.Text = "Item is empty"
	}

	bot.Send(msg)
}

func getLimits(bot *tgbotapi.BotAPI, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")

	limits := []string{"max_unfollow_per_day", "days_before_unfollow", "max_likes_to_account_per_session", "max_retry", "like.min", "like.count", "like.max", "follow.count", "follow.potency_ratio", "comment.min", "comment.count", "comment.max"}
	for _, limit := range limits {
		if limit == "follow.potency_ratio" {
			msg.Text += fmt.Sprintf("%s: %.2f\n", limit, viper.GetFloat64("limits."+limit))
		} else {
			msg.Text += limit + ": " + strconv.Itoa(viper.GetInt("limits."+limit)) + "\n"
		}
	}

	bot.Send(msg)
}

func updateLimits(bot *tgbotapi.BotAPI, limitStr string, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	s := strings.Split(limitStr, " ")
	limits := []string{"max_unfollow_per_day", "days_before_unfollow", "max_likes_to_account_per_session", "max_retry", "like.min", "like.count", "like.max", "follow.count", "follow.potency_ratio", "comment.min", "comment.count", "comment.max"}
	if len(s) != 2 {
		msg.Text = "/updatelimits limitname integer\nlimitname maybe one of: " + strings.Join(limits, ", ")
	} else {
		limit, count := s[0], s[1]

		if stringInStringSlice(limit, limits) {
			if limit == "follow.potency_ratio" {
				limitCount, _ := strconv.ParseFloat(count, 64)
				if limitCount >= -100 && limitCount <= 100 {
					viper.Set("limits."+limit, limitCount)
					viper.WriteConfig()
					msg.Text = "Limit updated"
				} else {
					msg.Text = "/updatelimits limitname float\ncount should be equal or greater than -100 and less or equal than 100"
				}
			} else {
				limitCount, _ := strconv.Atoi(count)
				if limitCount >= 0 && limitCount <= 10000 {
					viper.Set("limits."+limit, limitCount)
					viper.WriteConfig()
					msg.Text = "Limit updated"
				} else {
					msg.Text = "/updatelimits limitname integer\ncount should be equal or greater than 0 and less or equal than 10000"
				}
			}
		} else {
			msg.Text = "/updatelimits limitname integer\nlimitname maybe one of: " + strings.Join(limits, ", ")
		}
	}

	bot.Send(msg)
}

func getLastLikers() (result []string) {
	latest, _ := insta.LatestFeed()
	l := latest.Items
	if len(l) > 10 {
		l = latest.Items[0:10] //last 10 posts
	}
	for lindex := range l {
		// log.Println(item.ID, item.HasLiked, item.LikeCount)
		if l[lindex].LikeCount > 0 {
			likers, _ := insta.MediaLikers(l[lindex].ID)
			for index := range likers.Users {
				// log.Println(liker.Username, liker.HasAnonymousProfilePicture)
				if !stringInStringSlice(likers.Users[index].Username, result) {
					result = append(result, likers.Users[index].Username)
				}
			}
		}
	}
	// log.Println(len(result), result)
	return result
}

func likeFollowersPosts(db *bolt.DB) {
	timeline, _ := insta.Timeline("")
	length := len(timeline.Items)
	if length > 16 {
		length = 16
	}

	var usernames []string

	if length > 0 {
		items := timeline.Items[0:length]
		for index := range items {
			// log.Println(item.ID, item.Caption, item.User.Username)
			if !items[index].HasLiked {
				time.Sleep(5 * time.Second)
				insta.Like(items[index].ID)
				incStats(db, "like")

				usernames = append(usernames, items[index].User.Username)
			}
		}
		length = len(usernames)
		if length > 0 {
			usernames = sliceUnique(usernames)
			log.Println("liked", strings.Join(usernames, ", "))
		}
	}
}

func addWatching(bot *tgbotapi.BotAPI, db *bolt.DB, userid string, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	if len(userid) > 0 {
		err := setWatching(db, string(userid))
		if err != nil {
			msg.Text = "Already watching " + userid
		} else {
			msg.Text = "Added " + userid + " for watching"
		}
		bot.Send(msg)
	}
}

func watch(bot *tgbotapi.BotAPI, db *bolt.DB, args string, userID int64) {
	if args != "" {
		argsArray := strings.Fields(args)
		switch argsArray[0] {
		case "list":
			sendWatching(bot, db, userID)
		case "add":
			addWatching(bot, db, argsArray[1], userID)
		case "del":
			msg := tgbotapi.NewMessage(userID, "")
			deleteKeyFromBucket(db, "watching", argsArray[1])
			// need to add checks
			msg.Text = "Removed " + argsArray[1] + " from watching list"
			bot.Send(msg)
		}
	}
}

func sendWatching(bot *tgbotapi.BotAPI, db *bolt.DB, userID int64) {
	msg := tgbotapi.NewMessage(userID, "")
	watchingList, _ := getWatchingList(db)
	for _, userid := range watchingList {
		msg.Text += userid + "\n"
	}
	bot.Send(msg)
}

func sendQueueSize(bot *tgbotapi.BotAPI, db *bolt.DB, userID int64, bucket string) {
	msg := tgbotapi.NewMessage(userID, "")
	queuesize := bucketStats(db, bucket).KeyN
	msg.Text = strconv.Itoa(queuesize)
	bot.Send(msg)
}

func PercentageChange(old, new int) (delta float64) {
	diff := float64(new - old)
	delta = (diff / float64(old)) * 100
	return
}

func getWatchingUser(db *bolt.DB) (string, error) {
	var userid string
	err := db.View(func(tx *bolt.Tx) error {
		bk := tx.Bucket([]byte("watching"))
		if bk == nil {
			return fmt.Errorf("failed to find bucket")
		}
		var oldnumber int

		c := tx.Bucket([]byte("watching")).Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {

			oldnumber, _ = strconv.Atoi(string(v))
			if oldnumber == 0 {
				log.Printf("%s have %d followers, checking", k, oldnumber)
				userid = string(k)
				user, _ := insta.GetUserByUsername(string(k))
				var newnumber = user.User.FollowerCount
				updateDB(db, []byte("watching"), []byte(userid), []byte(strconv.Itoa(newnumber)))
				break
			} else {
				user, _ := insta.GetUserByUsername(string(k))

				var newnumber = user.User.FollowerCount
				if PercentageChange(oldnumber, newnumber) > 10 {
					userid = string(k)
					updateDB(db, []byte("watching"), []byte(userid), []byte(strconv.Itoa(newnumber)))
					break
				}
			}
		}
		return nil
	})
	fmt.Println(userid)
	return userid, err
}

func followQueueManager(db *bolt.DB) (startChan chan bool, outerChan, innerChan chan string, stopChan chan bool) {
	startChan = make(chan bool)
	outerChan = make(chan string)
	innerChan = make(chan string)
	stopChan = make(chan bool)
	go func() {
		for {
			select {
			case <-startChan:
				if !followQueueIsStarted.IsSet() {
					followQueueIsStarted.Set()
					go startFollowFromQueue(db, 10)
				} else {
					fmt.Println("can't start task, task already running!")
				}
			case msg := <-outerChan:
				fmt.Println("followqueue <- ", msg)
			}

		}
	}()
	return startChan, outerChan, innerChan, stopChan
}

func scrapFollowersFromUser(db *bolt.DB, username string) {

	user, err := insta.GetUserByUsername(username)
	if err != nil {
		fmt.Println(fmt.Sprintf("%s", err))
		return
	}
	if user.User.IsPrivate {
		userFriendShip, err := insta.UserFriendShip(user.User.ID)
		check(err)
		if !userFriendShip.Following {
			log.Printf("User profile \"%s\" is private and we are not following, can't process", username)
			return
		}
	}
	followers, err := insta.TotalUserFollowers(user.User.ID)
	check(err)
	var users = followers.Users
	for index := range users {
		if users[index].IsPrivate {
			log.Printf("%s is private, skipping\n", users[index].Username)
		} else {
			previoslyFollowed, _ := getFollowed(db, users[index].Username)
			if previoslyFollowed != "" {
				log.Printf("%s previously followed at %s, skipping\n", users[index].Username, previoslyFollowed)
			} else {
				log.Printf("Adding %s to queue)", users[index].Username)
				addToFollowQueue(db, users[index].Username)
			}
		}
	}
}

func startFollowFromQueue(db *bolt.DB, limit int) {
	var current = 0
	usersQueue := getUsersFromQueue(db, limit)
	for index := range usersQueue {
		current++
		user, err := insta.GetUserByUsername(usersQueue[index])
		if err != nil {
			log.Printf("[%d/%d] %s doesn't exist\n", current, limit, usersQueue[index])
			deleteKeyFromBucket(db, "followqueue", usersQueue[index])
			continue
		}
		userFriendShip, err := insta.UserFriendShip(user.User.ID)
		check(err)
		if !userFriendShip.Following {
			if user.User.IsPrivate {
				log.Printf("[%d/%d] %s is private, skipping follow\n", current, limit, usersQueue[index])
			} else {
				log.Printf("[%d/%d] Following %s\n", current, limit, usersQueue[index])
				resp, err := insta.Follow(user.User.ID)
				if err != nil {
					log.Println(err)
				} else {
					userFriendShip.Following = resp.FriendShipStatus.Following

					if !userFriendShip.Following {
						log.Println("Not followed")
					}
				}
			}
			if userFriendShip.Following {
				numFollowed++
				incStats(db, "refollow")
				setFollowed(db, usersQueue[index])
				time.Sleep(16 * time.Second)
			} else {
				time.Sleep(2 * time.Second)
			}
		} else {
			log.Printf("[%d/%d] Already following %s\n", current, limit, usersQueue[index])
		}
		deleteKeyFromBucket(db, "followqueue", usersQueue[index])
	}
}

// func likeFollowersStories(db *bolt.DB) {
// 	stories, _ := insta.GetReelsTrayFeed("")
// 	length := len(stories.Tray)
// 	if length > 10 {
// 		length = 10
// 	}

// 	var usernames []string

// 	if length > 0 {
// 		items := stories.Tray[0:length]
// 		for index := range items {
// 			// log.Println(item.ID, item.Caption, item.User.Username)
// 			if !items[index].Seen {
// 				time.Sleep(10 * time.Second)
// 				insta.media.Seen(items[index].ID)
// 				incStats(db, "seen")

// 				usernames = append(usernames, items[index].User.Username)
// 			}
// 		}
// 		length = len(usernames)
// 		if length > 0 {
// 			usernames = sliceUnique(usernames)
// 			log.Println("seen", strings.Join(usernames, ", "))
// 		}
// 	}
// }

func getStatus() (result string) {
	userinfo, err := insta.GetUserByUsername(insta.LoggedInUser.Username)
	if err != nil {
		log.Println("getStatus", insta.LoggedInUser.Username, err)
	} else {
		result = fmt.Sprintf("🖼%d, 👀%d, 🐾%d", userinfo.User.MediaCount, userinfo.User.FollowerCount, userinfo.User.FollowingCount)
	}

	return
}

func stringToBin(s string) (binString string) {
	for _, c := range s {
		binString = fmt.Sprintf("%s%b", binString, c)
	}
	return
}

func leftPad2Len(s string, padStr string, overallLen int) string {
	var padCountInt int
	padCountInt = 1 + ((overallLen - len(padStr)) / len(padStr))
	var retStr = strings.Repeat(padStr, padCountInt) + s
	return retStr[(len(retStr) - overallLen):]
}

func bin2int(binStr string) string {
	result, _ := strconv.ParseInt(binStr, 2, 64)
	return strconv.FormatInt(result, 10)
}

// Base64UrlCharmap - all posible characters
const Base64UrlCharmap = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// Returns data for url from media codes
func mediaFromCode(code string) string {

	base2 := ""
	for i := 0; i < len(code); i++ {
		base64 := strings.Index(Base64UrlCharmap, string(code[i]))
		str2bin := strconv.FormatInt(int64(base64), 2)
		sixbits := leftPad2Len(str2bin, "0", 6)
		base2 = base2 + sixbits
	}

	return bin2int(base2)
}
