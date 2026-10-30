# Test data for the position check

| File | What it is | Source | Author | Licence |
|---|---|---|---|---|
| clock-sony-917.jpg | A Sony clock radio showing 9:17. The 1280x960 original scaled to 640x480 and saved with Pillow at JPEG quality 82. | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:12-hour_digital_clock_radio.JPG) | Zginder | Public domain |
| t2-shake.csv | Shifts and scores the check measured on round 6's emulated cameras (run full2): four stretches of 150 checks with shake, exposure steps and night vision, and no move. Made by this project. | this project | watchglass | same as the code |

The tests move, turn, re-expose and cover the clock photo in code. The washer
photos the tests also use are the appliance recipe's samples in
`docs/recipes/samples`.
