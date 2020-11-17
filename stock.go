package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	log "github.com/sirupsen/logrus"
)

// ToDo - I don't think any of these need to be exported types anymore,
// Initially they were due to how I thought JSON was going to be loaded int

// Target - each online search is a target
type Target struct {
	Name       string
	URL        string
	ButtonText string
	Users      []*User
}

// User -
type User struct {
	Name string
	Cell string
}

// Twilio -
type Twilio struct {
	From string
	User string
	Pass string
	URL  string
}

// Timing config
type Timing struct {
	URLTimout time.Duration
	Looptime  time.Duration
}

// Result of a search
type Result struct {
	target  *Target
	matches int
}

/*
	Todo: detect missing configuration values, we only detect unexpected keys.
*/
func main() {
	file, err := os.OpenFile("stock.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal(err) // Normal log, that we couldnt open our fancy log file
	}
	defer file.Close()

	log.SetOutput(file)
	log.SetFormatter(&log.JSONFormatter{})
	log.SetLevel(log.InfoLevel)

	// Read in config from environment
	configJSON := os.Getenv("STOCKCONF")

	twilio := Twilio{}
	timing := Timing{}
	targets := make(map[string]*Target)
	users := make(map[string]*User)

	var config map[string]interface{}
	err = json.Unmarshal([]byte(configJSON), &config)
	if err != nil {
		log.Fatal(err)
	}

	// Twilio is just a flat map string:string so suck in all kv
	for k, v := range config["twilio"].(map[string]interface{}) {
		// Using a switch statement to be explicit about what is allowed
		switch k {
		case "from":
			twilio.From = v.(string)
		case "user":
			twilio.User = v.(string)
		case "pass":
			twilio.Pass = v.(string)
		case "url":
			twilio.URL = v.(string)
		default:
			log.Fatal("Unknown twilio configuration")
		}
	}
	// TODO: Verify that all required fields are set

	// Timing
	for k, v := range config["timing"].(map[string]interface{}) {
		switch k {
		case "urltimeout":
			timing.URLTimout = time.Duration(v.(float64)) * time.Second
		case "looptime":
			timing.Looptime = time.Duration(v.(float64)) * time.Second
		default:
			log.Fatal("Unknown timing config")
		}
	}
	// TODO: Verify that all required fields are set

	// Targets is an array of maps, we want to store as a map of maps
	for _, m := range config["targets"].([]interface{}) {
		target := Target{}
		for tk, tv := range m.(map[string]interface{}) {
			switch tk {
			case "name":
				target.Name = tv.(string)
			case "url":
				target.URL = tv.(string)
			case "button-text":
				target.ButtonText = tv.(string)
			default:
				log.Fatal("Unknown target configuration")
			}
		}
		targets[target.Name] = &target
	} // TODO: Verify that all required fields are set

	// Users is an array of maps, we want to store as a map of maps
	// Users also has an array called "Targets", which are targets the user is interested in

	//Targets is just a flat map string:string so suck in all kv
	for _, m := range config["users"].([]interface{}) {
		user := User{}
		for uk, uv := range m.(map[string]interface{}) {
			switch uk {
			case "name":
				user.Name = uv.(string)
				log.Info("Got user with name ", user.Name)
			case "cell":
				user.Cell = uv.(string)
			case "targets":
				for _, t := range uv.([]interface{}) {
					// Get the matching target and add the user
					targets[t.(string)].Users = append(targets[t.(string)].Users, &user)
				}
			default:
				log.Fatal("Unkown user configuration")
			}
		}
		users[user.Name] = &user
	} // TODO: Verify that all required fields are set

	// Ok so we have our config, now we need to go fetch the pages

	ticker := time.NewTicker(timing.Looptime)
	ch := make(chan *Result)

	// Loop every timing.Looptime and launch a crawler for each target
	for {
		select {
		case <-ticker.C:
			log.Info("Looping\n")
			for _, target := range targets {
				go crawl(target, timing.URLTimout, ch)
			}
		case res := <-ch:
			if res.matches > 0 {
				log.Info("Matches", res.target.Name, res.matches)

				// Crikey, there's stock - better notify our users!
				// No need to do so in serial, these things should be short lived, don't contain loops
				for _, user := range res.target.Users {
					go notify(user, res, &twilio)
				}
			}
		}
	}
}

// Crawl the URL from the target
// Look for the attributes
// If attributes found, send Target on channel
// If not, send nil
func crawl(t *Target, timeout time.Duration, ch chan *Result) {
	// hit the url
	// look for the attr

	result := Result{target: t}

	client := &http.Client{}
	client.Timeout = timeout

	req, err := http.NewRequest("GET", t.URL, nil)
	if err != nil {
		log.Fatal(err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.183 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		log.Warn(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Warn(t.Name, resp.Status)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	doc.Find("button").Each(func(i int, s *goquery.Selection) {
		//We found a matching attribute! yay!
		btntext := s.Text()
		if strings.HasPrefix(btntext, t.ButtonText) {
			result.matches++
		}
	})

	ch <- &result
}

//ToDo: Error handling (just use log.Fatal for now)
func notify(user *User, result *Result, twilio *Twilio) {
	snow := time.Now().Format(time.Stamp)
	message := fmt.Sprintf(
		"%s has %d items in stock\nTime: %s\n%s",
		result.target.Name,
		result.matches,
		snow,
		result.target.URL)

	client := &http.Client{}
	values := url.Values{}
	values.Set("To", user.Cell)
	values.Set("From", twilio.From)
	values.Set("Body", message)

	req, err := http.NewRequest("POST", twilio.URL, strings.NewReader(values.Encode()))
	if err != nil {
		log.Fatal(err)
	}
	req.SetBasicAuth(twilio.User, twilio.Pass)
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}

	if resp.StatusCode != 201 {
		var data map[string]interface{}
		decoder := json.NewDecoder(resp.Body)
		err := decoder.Decode(&data)
		if err == nil {
			fmt.Println(data["sid"])
		}
	} else {
		fmt.Println(resp.Status)
	}
}
