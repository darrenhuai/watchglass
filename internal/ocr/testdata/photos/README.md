# Real photos used as test fixtures

Each file is a crop of a freely licensed photo, recompressed small. The
tests say which box on it they use and why.

| File | What it shows | Source | Author | Licence |
|---|---|---|---|---|
| clock-philips-106.jpg | An LED alarm clock showing 1:06, photographed at an angle. A 417x358 crop round the digits (pixels 505,325 to 922,683 of the 1280px wide version), saved with Pillow at JPEG quality 85. | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:LED_seven_segment_display_on_a_digital_clock_(16041065485).jpg) | cogdogblog | [CC BY 2.0](https://creativecommons.org/licenses/by/2.0/) |

The crop is a change to the original. With Binarize set to 128 and the box
round the digits, this photo used to make the seven-segment decoder panic
(see `sevenseg_crash_test.go`).
