# rawstreamer

**rawstreamer** is a low latency audio streaming tool that connects to [JACK](http://jackaudio.org/) as the audio source. rawstreamer is written entirely in golang, and uses [go-jack](https://github.com/xthexder/go-jack) and [portaudio-go](https://code.google.com/p/portaudio-go/) for stream input and playback respectively.

rawstreamer is designed to minimize the audio delay from the input source. This means when you hit `Pause` on the audio source, you won't have to wait for 8 seconds of buffer to play before it stops on your local player. Streaming works very well on local networks, as well as over the internet if the bandwidth is available.

rawstreamer implements a custom metadata header at the beginning of the stream to send sample rate and bit depth information, but the header length is matched to be compatible with raw data streaming programs like `sox`.

## `rawstreamer`

### Requirements
- A linux system
- [Jack Audio Connection Kit](http://jackaudio.org/)

### Building
1. `go clone github.com/xthexder/rawstreamer`
2. `cd rawstreamer/streamer`
3. `go get .`
4. `go build rawstreamer.go`

### Usage

The default settings will stream on port `:5253`, 24bit signed-int little-endian PCM, but these can be changed with command line flags. The sample rate specified by JACK will always be used.

To stream all system output, the `-mirror` flag can be used:  
`./rawstreamer -mirror system` (where `system` is jack system output port)

To stream a specific program, you can specify the program name with `-proc-name`:  
`./rawstreamer -proc-name mplayer` (programs using alsa-plugins only)

To stream a specific jack port name, you can specify it with `-source`:  
`./rawstreamer -source PulseAudio`

## `rawplayer`

### Requirements
- [Portaudio](http://www.portaudio.com/)

### Building
1. `go clone github.com/xthexder/rawstreamer`
2. `cd rawstreamer/player`
3. `go get .`
4. `go build rawplayer.go`

### Usage

`./rawplayer hostname:port [buffer-duration]`

Example:  
`./rawplayer localhost:5253 50ms`

When connecting to a `rawstreamer` server, the `rawplayer` client will automatically detect the stream sample rate and encoding information, so they do not need to be specified. Depending on the quality of the network between the client and server, you may want to change the buffer size used by the client, but the default `100ms` buffer should work fine over most networks. Increasing the buffer size will reduce buffer underflows, but will increase the audio delay.

The bar displayed by the player shows the current state of the buffer:  
`18363 / 24001 [========================================>------------] 76.51 %`  
If the buffer empties, then the audio will dropout until the buffer has filled again. This should help you gauge how big your buffer need to be to eliminate dropouts.