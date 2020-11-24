package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
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
	Name     string
	Cell     string
	Cooldown int
	History  map[string]time.Time
}

// Twilio -
type Twilio struct {
	From string
	User string
	Pass string
	URL  string
}

// Config config
type Config struct {
	URLTimout time.Duration
	Looptime  time.Duration
	OSNotify  bool
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
	log.SetLevel(log.DebugLevel) // TODO: Make this a config option

	log.Debug(fmt.Sprintf("[%s] operating system detected", runtime.GOOS))

	// Read in config from environment
	configJSON := os.Getenv("STOCKCONF")
	if configJSON == "" {
		log.Fatal("$STOCKCONF is not set.")
	}

	twilio := Twilio{}
	Config := Config{OSNotify: false}
	targets := make(map[string]*Target)
	users := make(map[string]*User)

	var config map[string]interface{}
	err = json.Unmarshal([]byte(configJSON), &config)
	if err != nil {
		log.Fatal(err)
	}

	// Validate that the config has the correct high level fields
	for k := range config {
		switch k {
		case "targets": // ok
		case "users": // ok
		case "twilio": // ok
		case "config": // ok
		default: // bad
			log.Fatal(fmt.Sprintf("Invalid top level configuration key [%s]", k))
		}
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
	log.Debug(twilio)
	// TODO: Verify that all required fields are set

	// Populate the Config config struct
	for k, v := range config["config"].(map[string]interface{}) {
		switch k {
		case "urltimeout":
			Config.URLTimout = time.Duration(v.(float64)) * time.Second
		case "looptime":
			Config.Looptime = time.Duration(v.(float64)) * time.Second
		case "osnotify":
			Config.OSNotify = v.(bool)
		default:
			log.Fatal("Unknown key in config")
		}
	}
	log.Debug(Config)
	// TODO: Verify that all required fields are set

	// Populate Targets map[string]*Target
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
	log.Debug(targets)

	// Populate Users
	for _, m := range config["users"].([]interface{}) {
		user := User{History: make(map[string]time.Time)}
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
					if _, ok := targets[t.(string)]; ok {
						targets[t.(string)].Users = append(targets[t.(string)].Users, &user)
					} else {
						log.Error(fmt.Sprintf("User [%s] has target [%s] configured but no corresponding target exists", user.Name, t.(string)))
					}
				}
			case "cooldown":
				user.Cooldown = int(uv.(float64))
			default:
				log.Fatal("Unkown user configuration")
			}
		}
		users[user.Name] = &user
	} // TODO: Verify that all required fields are set
	log.Debug(users)

	/*
	 * Config stuff is all loaded in at this point
	 * Next, setup ticker which will govern how quickly we the search will loop
	 */
	ticker := time.NewTicker(Config.Looptime)
	ch := make(chan *Result)

	// Loop every Config.Looptime and launch a crawler for each target
	for {
		select {
		case <-ticker.C:
			for _, target := range targets {
				go crawl(target, Config.URLTimout, ch)
			}
		case res := <-ch:
			log.Info(fmt.Sprintf("%s had [%d] stock", res.target.Name, res.matches))

			if res.matches > 0 {
				// Crikey, there's stock - better notify our users!
				now := time.Now()
				for _, user := range res.target.Users {
					// If this target URL is in cooldown for this user then don't send a notification

					sendNotification := false
					then, isCooling := user.History[res.target.URL]
					if isCooling == true {
						delta := now.Sub(then).Seconds()
						if int(delta) >= user.Cooldown {
							sendNotification = true
						}
					} else {
						sendNotification = true
					}

					if sendNotification == true {
						go notify(user, res, &twilio)
						if Config.OSNotify == true {
							go osnotify(user, res)
						}
					}

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

// osnotify will use native OS notification features
// to tell the user about stock found
func osnotify(user *User, result *Result) {
	if runtime.GOOS == "darwin" {
		// osascript -e 'display notification "Lorem ipsum dolor sit amet" with title "Title"'
		cmdString := fmt.Sprintf("display notification \"Has %d Stock\" sound name \"Hero\" with title \"%s\" ",
			result.matches,
			result.target.Name)
		cmd := exec.Command("osascript", "-e", cmdString)
		err := cmd.Run()
		if err != nil {
			log.Error(err)
		}
	}
}

//TODO: Error handling (just use log.Fatal for now)
func notify(user *User, result *Result, twilio *Twilio) {
	now := time.Now()
	snow := now.Format(time.Stamp)
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
			// The message was sent correctly, so now we add a note of the current time to the history for this user, using the URL of the target.
			// Ideally we'd track the actual button, not the site, because other buttons may become available during the cooldown period... Not super likely though.
			user.History[result.target.URL] = now // We set this near the entrypoint to the function
		}
	} else {
		log.Warn(resp.Status) // resp.Status is a string, whereas resp.StatusCode is an int
	}
}
