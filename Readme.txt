GoStock is designed to take a specially crafted URL, and look for "Buy Buttons"
You can use this to send you SMS alerts (From the Twilio service) when something
you are interested in becomes available to purchase online.

GoStock is configured by an environment variable STOCKCONF that includes a JSON
configuration. There are two example json files in this repository. The example
configurations work for finding Nvidia 3080 stock on BestBuy and NewEgg.

The reason there are two configs is that most of the time you won't get output from 
the application, and sometimes you just need some other URL, that does have stock
to ensure that everything is working end-to-end (that the website isn't blocking you etc.)

To get going, rename the json example file and add real Twilio details.

To load the json into an environment variable run the following:
```
export STOCKCONF=$(<config.json)
```

To run the application:
```
go run ./stock.go
```

Logs are stored in stock.txt
```
tail -f stock.txt
```

Note, there's no "cooloff" config at the moment, you'll get a text message every time it loops while $company has stock.
This is very annoying if you misconfigure your searches.
