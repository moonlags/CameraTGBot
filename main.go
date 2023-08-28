package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NicoNex/echotron/v3"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

type event struct {
	X      int
	Y      int
	Hour   int
	Minute int
	Active bool
	Sunset bool
}

type Bot struct {
	chatID  int64
	isGuest bool
	state   stateFn
	Event   event
	echotron.API
	mu sync.Mutex
}

type stateFn func(*echotron.Update) stateFn

var dsp *echotron.Dispatcher
var CameraPosX int
var task_count int
var weekday string = "BlaBlaDay"
var hoursunset int
var minutesunset int
var guestpass string
var randsrc *rand.Rand

const queue_cap = 5

func newBot(chatID int64) echotron.Bot {
	bot := &Bot{
		chatID: chatID,
		API:    echotron.NewAPI(os.Getenv("TOKEN")),
	}

	bot.state = bot.handleMessage
	go bot.selfDestruct(time.After(time.Hour * 8))
	return bot
}

func (b *Bot) selfDestruct(timech <-chan time.Time) {
	<-timech
	if b.Event.Active {
		b.state = b.handleMessage
		go b.selfDestruct(time.After(time.Hour * 8))
	} else {
		dsp.DelSession(b.chatID)
	}
}

func (b *Bot) Update(update *echotron.Update) {
	if update.Message == nil {
		return
	}

	log.Info().Str("Text", update.Message.Text).Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Msg("")

	b.state = b.state(update)
}

func (b *Bot) handleEventCreate(update *echotron.Update) stateFn {
	state, ok := b.checkCommands(update)
	if ok {
		return state
	}

	data := strings.Fields(update.Message.Text)
	if len(data) != 4 {
		log.Warn().Str("data", update.Message.Text).Msg("Coordinates and time are not 4 numbers.")
		_, err := b.SendMessage(fmt.Sprintf("%v, please specify valid info in format \"X Y Hours Minutes\" to create an event 📷", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleEventCreate
	}
	x, err := strconv.Atoi(data[0])
	y, err2 := strconv.Atoi(data[1])
	hour, err3 := strconv.Atoi(data[2])
	minute, err4 := strconv.Atoi(data[3])
	if err2 != nil || err != nil || err3 != nil || err4 != nil {
		log.Warn().Strs("data", data).Msg("X, Y, Hour or Minute are not numbers.")
		_, err := b.SendMessage(fmt.Sprintf("%v, please specify valid info in format \"X Y Hours Minutes\" to create an event 📷", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleEventCreate
	}

	hour = hour % 24
	minute = minute % 60
	if hour < 0 {
		log.Warn().Int("hour", hour).Msg("Hours are negative.")
		_, err := b.SendMessage(fmt.Sprintf("%v, hours cant be negative number [🛑]", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleEventCreate
	} else if minute < 0 {
		log.Warn().Int("minute", minute).Msg("Minutes are negative.")
		_, err := b.SendMessage(fmt.Sprintf("%v, minutes cant be negative number [🛑]", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleEventCreate
	}

	if x < 0 || x > 360 {
		log.Warn().Int("x", x).Msg("X is greater than 360 or negative.")
		_, err := b.SendMessage(fmt.Sprintf("%v, X coordinate should be greater than 0, but smaller than 360 [🛑]", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleEventCreate
	} else if y < 0 || y > 90 {
		log.Warn().Int("y", y).Msg("Y is greater than 90 or negative.")
		_, err := b.SendMessage(fmt.Sprintf("%v, Y coordinate should be greater than 0, but smaller than 90 [🛑]", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleEventCreate
	}

	_, err = b.SendMessage(fmt.Sprintf("%v, event (X: %v Y: %v %v:%v Sunset:%v) created 🎉", update.Message.From.FirstName, x, y, hour, minute, b.Event.Sunset), b.chatID, nil)
	if err != nil {
		log.Error().Err(err).Msg("Failed to send message.")
		time.Sleep(10 * time.Second)
	}

	b.Event = event{X: x, Y: y, Hour: hour, Minute: minute, Active: true}

	log.Info().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Ints("cords", []int{x, y}).Ints("time", []int{hour, minute}).Msg("Created event.")

	go b.runEvent()

	return b.handleLogin
}

func (b *Bot) runEvent() {
	for range time.Tick(time.Second) {
		timenow := time.Now()
		if !b.Event.Sunset && timenow.Hour() == b.Event.Hour && timenow.Minute() == b.Event.Minute && timenow.Second() == 0 {
			task_count++
			log.Info().Ints("cords", []int{b.Event.X, b.Event.Y}).Msg("Doing event photo.")
			go b.AccessCamera(b.Event.X, b.Event.Y)
		} else if !b.Event.Active {
			log.Info().Msg("Aborting event.")
			return
		} else if b.Event.Sunset && timenow.Hour() == hoursunset && timenow.Minute() == minutesunset && timenow.Second() == 0 {
			task_count++
			log.Info().Ints("cords", []int{b.Event.X, b.Event.Y}).Msg("Doing sunset event photo.")
			go b.AccessCamera(b.Event.X, b.Event.Y)
		}
	}
}

func (b *Bot) checkCommands(update *echotron.Update) (stateFn, bool) {
	if update.Message.Text == "/help" && b.isGuest {
		if _, err := b.SendMessage("/help - Get a list of commands 📜\n/photo - Take a photo from camera 📷\n/dice - Throw a dice and take a photo 🎲\n/sunsettime - Get sunset time 🌆🕘", b.chatID, nil); err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleLogin, true
	} else if update.Message.Text == "/help" {
		_, err := b.SendMessage("/help -  Get a list of commands 📜\n/photo - Take a photo from camera 📷\n/dice - Throw a dice and take a photo 🎲\n/eventcreate - Create an event 🎉\n/eventdelete - Delete an event 🔴\n/eventsunset - Create sunset event 🌆\n/sunsettime - Get sunset time 🌆🕙\n/guestpass - Get guest password 🔐", b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleLogin, true
	} else if update.Message.Text == "/photo" {
		_, err := b.SendMessage(fmt.Sprintf("%v, please specify coordinates X Y 🕹 in degrees to turn camera 📷 and take a picture 🖼", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handlePhoto, true
	} else if update.Message.Text == "/dice" {
		data, err := b.SendDice(b.chatID, "🎲", nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send dice.")
			time.Sleep(10 * time.Second)
			return b.handleLogin, true
		}
		data2, err := b.SendDice(b.chatID, "🎲", nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send dice.")
			time.Sleep(10 * time.Second)
			return b.handleLogin, true
		}
		if task_count+1 > queue_cap {
			_, err := b.SendMessage("Sorry, queue is full. Try again later 🕙", b.chatID, nil)
			log.Warn().Int("task_count", task_count).Msg("Queue is full.")
			if err != nil {
				log.Error().Err(err).Msg("Failed to send message.")
				time.Sleep(10 * time.Second)
			}
			return b.handleLogin, true
		}

		x := 360 / 6 * data.Result.Dice.Value
		y := 90 / 6 * data2.Result.Dice.Value

		time.Sleep(5 * time.Second)

		log.Info().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Ints("cords", []int{x, y}).Msg("Doing dice photo.")

		task_count++
		go b.AccessCamera(x, y)

		_, err = b.SendMessage(fmt.Sprintf("%v, doing photo 🖼 on coordinates X: %v Y: %v, please wait 🕙", update.Message.From.FirstName, x, y), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}

		return b.handleLogin, true
	} else if update.Message.Text == "/eventcreate" {
		if b.isGuest {
			log.Warn().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Msg("Guest can not do that.")
			if _, err := b.SendMessage(update.Message.From.FirstName+", you can not do that as guest [🛑]", b.chatID, nil); err != nil {
				log.Error().Err(err).Msg("Failed to send message.")
				time.Sleep(10 * time.Second)
			}
			return b.handleLogin, true
		}

		if b.Event.Active {
			_, err := b.SendMessage(fmt.Sprintf("%v, delete your existing event first (X: %v Y: %v %v:%v Sunset:%v) 🎉", update.Message.From.FirstName, b.Event.X, b.Event.Y, b.Event.Hour, b.Event.Minute, b.Event.Sunset), b.chatID, nil)
			if err != nil {
				log.Error().Err(err).Msg("Failed to send message.")
				time.Sleep(10 * time.Second)
			}
			log.Warn().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Msg("User have event already.")
			return b.handleLogin, true
		}

		_, err := b.SendMessage(fmt.Sprintf("%v, event will send you photo 🖼 everyday at exact time, to create an event send information in format \"X Y Hours Minutes\" 😁", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleEventCreate, true
	} else if update.Message.Text == "/eventdelete" {
		if !b.Event.Active {
			_, err := b.SendMessage(fmt.Sprintf("%v, you have no existing event [🛑]", update.Message.From.FirstName), b.chatID, nil)
			if err != nil {
				log.Error().Err(err).Msg("Failed to send message.")
				time.Sleep(10 * time.Second)
			}
			log.Warn().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Msg("User have no events.")
			return b.handleLogin, true
		}

		_, err := b.SendMessage(fmt.Sprintf("%v, deleted your existing event (X: %v Y: %v %v:%v Sunset:%v) 🎉", update.Message.From.FirstName, b.Event.X, b.Event.Y, b.Event.Hour, b.Event.Minute, b.Event.Sunset), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		log.Info().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Ints("cords", []int{b.Event.X, b.Event.Y}).Ints("time", []int{b.Event.Hour, b.Event.Minute}).Bool("sunset", b.Event.Sunset).Msg("Deleted event.")
		b.Event.Active = false

		return b.handleLogin, true
	} else if update.Message.Text == "/eventsunset" {
		if b.isGuest {
			log.Warn().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Msg("Guest can not do that.")
			if _, err := b.SendMessage(update.Message.From.FirstName+", you can not do that as guest [🛑]", b.chatID, nil); err != nil {
				log.Error().Err(err).Msg("Failed to send message.")
				time.Sleep(10 * time.Second)
			}
			return b.handleLogin, true
		}

		if b.Event.Active {
			log.Warn().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Msg("User already have an event.")
			if _, err := b.SendMessage(update.Message.From.FirstName+", please delete your existing event first 🎉", b.chatID, nil); err != nil {
				log.Error().Err(err).Msg("Failed to send message.")
				time.Sleep(10 * time.Second)
			}
			return b.handleLogin, true
		}
		if _, err := b.SendMessage("Enter X and Y coordinate to create sunset event 🌆", b.chatID, nil); err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleSunset, true
	} else if update.Message.Text == "/sunsettime" {
		if _, err := b.SendMessage(update.Message.From.FirstName+", today you can see sunset at "+fmt.Sprint(hoursunset)+":"+fmt.Sprint(minutesunset), b.chatID, nil); err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleLogin, true
	} else if update.Message.Text == "/guestpass" {
		if b.isGuest {
			log.Warn().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Msg("Guest can not do that.")
			if _, err := b.SendMessage(update.Message.From.FirstName+", you can not do that as guest [🛑]", b.chatID, nil); err != nil {
				log.Error().Err(err).Msg("Failed to send message.")
				time.Sleep(10 * time.Second)
			}
			return b.handleLogin, true
		}
		if _, err := b.SendMessage(update.Message.From.FirstName+", guest password 🔐 for next 8 hours is "+guestpass, b.chatID, nil); err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleLogin, true
	}
	return nil, false
}

func GenGuestPass(dur time.Duration) {
	for range time.Tick(dur) {
		guestpass = fmt.Sprint(randsrc.Int())
		log.Info().Str("password", guestpass).Msg("generated new guest password.")
	}
}

func (b *Bot) handleSunset(update *echotron.Update) stateFn {
	if state, ok := b.checkCommands(update); ok {
		return state
	}
	cords := strings.Fields(update.Message.Text)
	if len(cords) != 2 {
		log.Warn().Str("cords", update.Message.Text).Msg("Coordinates are not two numbers.")
		if _, err := b.SendMessage(update.Message.From.FirstName+", please enter two coordinates 🕹", b.chatID, nil); err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleSunset
	}
	x, err := strconv.Atoi(cords[0])
	y, err2 := strconv.Atoi(cords[1])
	if err2 != nil || err != nil {
		log.Warn().Strs("cords", cords).Msg("X or Y is not a number.")
		_, err := b.SendMessage(update.Message.From.FirstName+", please specify valid coordinates X Y 🕹 in degrees to create an event 📷", b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handlePhoto
	}

	if x < 0 || x > 360 {
		log.Warn().Int("x", x).Msg("X is greater than 360 or negative.")
		_, err := b.SendMessage(update.Message.From.FirstName+", X coordinate should be greater than 0, but smaller than 360 [🛑]", b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handlePhoto
	} else if y < 0 || y > 90 {
		log.Warn().Int("y", y).Msg("Y is greater than 90 or negative.")
		_, err := b.SendMessage(update.Message.From.FirstName+", Y coordinate should be greater than 0, but smaller than 90 [🛑]", b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handlePhoto
	}

	if _, err := b.SendMessage("Created sunset 🌆 event at coordinates "+fmt.Sprint(x)+" "+fmt.Sprint(y), b.chatID, nil); err != nil {
		log.Error().Err(err).Msg("Failed to send message.")
		time.Sleep(10 * time.Second)
		return b.handleLogin
	}
	log.Info().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Ints("cords", []int{x, y}).Msg("Created sunset event.")

	b.Event = event{X: x, Y: y, Active: true, Sunset: true}

	go b.runEvent()

	return b.handleLogin
}

func (b *Bot) handleLogin(update *echotron.Update) stateFn {
	state, ok := b.checkCommands(update)
	if ok {
		return state
	}

	log.Info().Str("cmd", update.Message.Text).Msg("Unknown command.")
	if _, err := b.SendMessage(update.Message.From.FirstName+", I dont understand command: "+update.Message.Text, b.chatID, nil); err != nil {
		log.Error().Err(err).Msg("Failed to send message.")
		time.Sleep(10 * time.Second)
	}
	return b.handleLogin
}

func (b *Bot) handleMessage(update *echotron.Update) stateFn {
	if update.Message.Text == guestpass {
		log.Info().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Msg("Logged in as guest.")
		if _, err := b.SendMessage("Welcome back, "+update.Message.From.FirstName+", I am ready to work, please send me a \"/photo\" command to take a picture 🖼", b.chatID, nil); err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		b.isGuest = true
		return b.handleLogin
	} else if update.Message.Text == os.Getenv("PASSWORD") {
		log.Info().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Msg("Logged in.")
		_, err := b.SendMessage("Welcome back, "+update.Message.From.FirstName+", I am ready to work, please send me a \"/photo\" command to take a picture 🖼", b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleLogin
	} else {
		_, err := b.SendMessage("Hello "+update.Message.From.FirstName+" 🖐,I am ready to take some photos 📷. Please send me your password😉", b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
	}
	return b.handleMessage
}

func (b *Bot) AccessCamera(x, y int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	defer func() { task_count -= 1 }()

	cmd := exec.Command("./motor_driver.bin", fmt.Sprint(x), fmt.Sprint(y), "False", fmt.Sprint(CameraPosX), "3", "wget -N -P . http://127.0.0.1:8080/photoaf.jpg")
	err := cmd.Run()
	if err != nil {
		b.SendMessage("Cant access motor_driver [🛑], try again later 🕑", b.chatID, nil)
		log.Error().Err(err).Msg("Failed to access motor_driver.")
		return
	}

	CameraPosX = x
	opts := &echotron.PhotoOptions{Caption: fmt.Sprintf("X: %v Y: %v", x, y)}
	f, err := os.Open("photoaf.jpg")
	if err != nil {
		b.SendMessage("Cant get photo [🛑], try again later 🕙", b.chatID, nil)
		phoneinit := exec.Command("./phone_init.sh")
		phoneinit.Run()
		log.Error().Err(err).Msg("Error accured: initialized phone.")
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		b.SendMessage("Cant read image file [🛑], try again later 🕘", b.chatID, nil)
		log.Error().Err(err).Msg("Failed to read image file.")
		return
	}

	_, err = b.SendPhoto(echotron.NewInputFileBytes("photoaf.jpg", data), b.chatID, opts)
	if err != nil {
		b.SendMessage("Cant send photo [🛑], try again later 🕞", b.chatID, nil)
		log.Error().Err(err).Msg("Cant send photo.")
	}
	os.Remove("photoaf.jpg")
}

func (b *Bot) handlePhoto(update *echotron.Update) stateFn {
	state, ok := b.checkCommands(update)
	if ok {
		return state
	}

	cords := strings.Fields(update.Message.Text)
	if len(cords) != 2 {
		log.Warn().Str("cords", update.Message.Text).Msg("Coordinates are not two numbers.")
		_, err := b.SendMessage(fmt.Sprintf("%v, please specify coordinates X Y 🕹 in degrees to turn camera 📷", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handlePhoto
	}
	x, err := strconv.Atoi(cords[0])
	y, err2 := strconv.Atoi(cords[1])
	if err2 != nil || err != nil {
		log.Warn().Strs("cords", cords).Msg("X or Y is not a number.")
		_, err := b.SendMessage(fmt.Sprintf("%v, please specify coordinates X Y 🕹 in degrees to turn camera 📷", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handlePhoto
	}

	if x < 0 || x > 360 {
		log.Warn().Int("x", x).Msg("X is greater than 360 or negative.")
		_, err := b.SendMessage(fmt.Sprintf("%v, X coordinate should be greater than 0, but smaller than 360 [🛑]", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handlePhoto
	} else if y < 0 || y > 90 {
		log.Warn().Int("y", y).Msg("Y is greater than 90 or negative.")
		_, err := b.SendMessage(fmt.Sprintf("%v, Y coordinate should be greater than 0, but smaller than 90 [🛑]", update.Message.From.FirstName), b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handlePhoto
	} else if task_count+1 > queue_cap {
		log.Warn().Int("task_count", task_count).Msg("Queue is full.")
		_, err := b.SendMessage("Sorry, queue is full. Try again later 🕙", b.chatID, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to send message.")
			time.Sleep(10 * time.Second)
		}
		return b.handleLogin
	}

	log.Info().Strs("user", []string{update.Message.From.FirstName, update.Message.From.LastName, update.Message.From.Username}).Ints("cords", []int{x, y}).Msg("Doing photo.")

	task_count++
	go b.AccessCamera(x, y)

	_, err = b.SendMessage(fmt.Sprintf("%v, added your request to the queue, please wait 🕙", update.Message.From.FirstName), b.chatID, nil)
	if err != nil {
		log.Error().Err(err).Msg("Failed to send message.")
		time.Sleep(10 * time.Second)
	}

	return b.handleLogin
}

func LogsControl() {
	var file *os.File
	var err error

	for {
		timenow := time.Now()
		if weekday != timenow.Weekday().String() {
			weekday = timenow.Weekday().String()

			log.Info().Str("weekday", weekday).Msg("New day started, closing logs file.")

			file.Close()

			file, err = os.Create(fmt.Sprintf("logs/%v_%v_%v.txt", timenow.Day(), timenow.Month(), timenow.Year()))
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to create new logs file.")
			}

			log.Logger = log.Output(file)

			dir, err := os.ReadDir("logs/")
			if err != nil {
				log.Error().Err(err).Msg("Failed to read logs/ directory.")
				continue
			}

			if len(dir) >= 11 {
				timeoldest := timenow
				var nameoldest string

				for _, v := range dir {
					info, err := v.Info()
					if err != nil {
						panic(err)
					}

					if info.ModTime().Before(timeoldest) {
						timeoldest = info.ModTime()
						nameoldest = info.Name()
					}
				}
				log.Info().Str("file", nameoldest).Msg("Deleting oldest file.")
				err = os.Remove("logs/" + nameoldest)
				if err != nil {
					log.Error().Err(err).Str("file", nameoldest).Msg("Failed to delete file.")
					continue
				}
			}

			req, err := http.NewRequest("GET", "https://api.sunrise-sunset.org/json?lat=56.968&lng=23.77038", nil)
			if err != nil {
				log.Error().Err(err).Msg("Failed to create http request.")
				continue
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Error().Err(err).Msg("Failed to complete request.")
				continue
			}

			data, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Error().Err(err).Msg("Failed to read response body")
				continue
			}
			resp.Body.Close()

			var jsondata map[string]interface{}
			err = json.Unmarshal(data, &jsondata)
			if err != nil {
				log.Error().Err(err).Msg("Failed to unmarshal json data.")
				continue
			}

			var results map[string]interface{} = jsondata["results"].(map[string]interface{})
			var sunset string = results["sunset"].(string)
			parseSunsetTime(sunset)
		}
		time.Sleep(time.Minute * 5)
	}
}

func parseSunsetTime(sunset string) {
	data := strings.Split(sunset, ":")
	hours, err := strconv.Atoi(data[0])
	if err != nil {
		log.Error().Err(err).Str("hours", data[0]).Msg("Failed to parse sunset hours.")
		return
	}
	minutes, err := strconv.Atoi(data[1])
	if err != nil {
		log.Error().Err(err).Str("minutes", data[1]).Msg("Failed to parse sunset minutes.")
		return
	}

	log.Info().Ints("time", []int{hours + 15, minutes}).Msg("Parsed sunset time.")
	hoursunset = hours + 15
	minutesunset = minutes
}

func init() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("Cant load env variables.")
	}
	randsrc = rand.New(rand.NewSource(time.Now().Unix()))

	go LogsControl()

	cmd := exec.Command("./motor_driver.bin", "0", "0", "True", "0", "3", "")
	err = cmd.Run()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize camera.")
	}

	log.Info().Msg("Initialized camera to X: 0 coordinate.")

	guestpass = fmt.Sprint(randsrc.Int())
}

func main() {
	go GenGuestPass(time.Hour * 8)

	dsp = echotron.NewDispatcher(os.Getenv("TOKEN"), newBot)

	log.Info().Msg("Created bot dispacther.")

	for {
		log.Error().Err(dsp.Poll()).Msg("Poll error accured.")

		time.Sleep(5 * time.Second)
	}
}
