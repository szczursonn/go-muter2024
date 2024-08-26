# go-muter2024

script for muting people when playing amogus

## Config (env variables)

.env file is automatically loaded

| key          | explanation                                    |
| ------------ | ---------------------------------------------- |
| MUTER_PREFIX | prefix to use in commands, default: $          |
| MUTER_TOKENS | period-seperated list of discord tokens        |
| MUTER_DEBUG  | if set, more verbose logging + logging to file |

## how it works

First token in MUTER_TOKENS list is used to connect to discord with websocket and listen to messages  
When request comes, it is handled by all clients that have permissions for mute (with the rest api)  
This is done to circumvent extremely harsh rate limits for voice state updates
