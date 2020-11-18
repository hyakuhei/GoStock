package main

import (
	"encoding/json"
	"fmt"
	"net"
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
	log.SetLevel(log.InfoLevel) // TODO: Make this a config option

	// Read in config from environment
	configJSON := os.Getenv("STOCKCONF")
	if configJSON == "" {
		log.Fatal("$STOCKCONF is not set.")
	}

	twilio := Twilio{}
	timing := Timing{}
	targets := make(map[string]*Target)
	users := make(map[string]*User)

	var config map[string]interface{}
	err = json.Unmarshal([]byte(configJSON), &config)
	if err != nil {
		log.Fatal(err)
	}

	// Populate twilio struct
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
		default: // Catch unknown config options
			log.Fatal("Unknown twilio configuration")
		}
	}
	// TODO: Verify that all required fields are set

	// Populate the timing config struct
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

	// Populate Targets struct
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

	// Populate Users
	for _, m := range config["users"].([]interface{}) {
		user := User{}
		for uk, uv := range m.(map[string]interface{}) {
			switch uk {
			case "name":
				user.Name = uv.(string)
				log.Info("Loaded user ", user.Name)
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

	/*
	 * Config stuff is all loaded in at this point
	 * Next, setup ticker which will govern how quickly we the search will loop
	 */
	ticker := time.NewTicker(timing.Looptime)
	ch := make(chan *Result)

	// Loop every timing.Looptime and launch a crawler for each target
	for {
		select {
		case <-ticker.C:
			for _, target := range targets {
				go crawl(target, timing.URLTimout, ch)
			}
		case res := <-ch:
			log.Info(fmt.Sprintf("%s had [%d] stock", res.target.Name, res.matches))

			if res.matches > 0 {
				// Crikey, there's stock - better notify our users!
				for _, user := range res.target.Users {
					go notify(user, res, &twilio)
				}
			}
		}
	}
}

// Crawl the URL from the target
// Look for the attributes
// Count the number of matches we find in a Result struct and return it.
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

	//TODO: Allow user to set the user-agent string via config
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.183 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		switch err := err.(type) {
		case net.Error:
			if err.Timeout() {
				log.Warn("Timeout accessing ", t.URL)
				ch <- &result
				return
			}
		case *url.Error:
			log.Error("URL Error", t.URL)
			ch <- &result
			return
		default:
			log.Error(err)
			ch <- &result
			return
		}
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

//TODO: Error handling (just use log.Fatal for now)
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

	// Prepare the POST request with form values (set above), headers and auth
	req, err := http.NewRequest("POST", twilio.URL, strings.NewReader(values.Encode()))
	if err != nil {
		log.Fatal(err)
	}
	req.SetBasicAuth(twilio.User, twilio.Pass)
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	// Attempt to make the request
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}

	// Check the outcome of the request
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Debug(resp.Status)
		var data map[string]interface{}
		decoder := json.NewDecoder(resp.Body)
		err := decoder.Decode(&data)
		if err == nil {
			log.Info("Twilio message dispatched", data["sid"])
		}
	} else {
		log.Warn(resp.Status) // resp.Status is a string, whereas resp.StatusCode is an int
	}
}
