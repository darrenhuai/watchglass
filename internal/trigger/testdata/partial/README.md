# Partly read seven-segment displays

Crops of real photos, cut to the box a watch reads (the region given below, as imgproc.Crop cuts it) and saved as PNG. The seven-segment decoder misses a digit on each of the first four, and the tests check that a numeric watch skips those readings instead of acting on the number around the `?`.

| File | Display shows | Decoder reads | Region in the photo | Photo | Author | Licence |
|---|---|---|---|---|---|---|
| car-71-0.png | 71.0 | `??.0` | x 0.37, y 0.40, w 0.12, h 0.08 | [car climate display](https://www.flickr.com/photos/97719890@N00/4501088139) | stevendamron | [CC BY 2.0](https://creativecommons.org/licenses/by/2.0/) |
| wall-15-5.png | 15.5 | `?5.5` | x 0.41, y 0.17, w 0.19, h 0.12 | [Heizungsthermostat mit Temperaturanzeige](https://commons.wikimedia.org/wiki/File:Heizungsthermostat_mit_Temperaturanzeige.jpg) | SchmiAlf | [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/) |
| hama-22-3.png | 22.3 | `?.3` | x 0.56, y 0.44, w 0.095, h 0.065 | [Hama TH50](https://commons.wikimedia.org/wiki/File:Hama_TH50.jpg) (the 1280 px version) | Morn | [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/); this crop is under the same licence |
| vaillant-22-5-a.png | 22.5 | `22.5` | x 0.43, y 0.34, w 0.17, h 0.11 | [Vaillant thermostat](https://www.flickr.com/photos/34107995@N00/441970348) | www.trek.today | [CC BY 2.0](https://creativecommons.org/licenses/by/2.0/) |
| vaillant-22-5-b.png | 22.5 | `?2.5` | the same region of the same photo enlarged by 1/0.88 about the box centre, which is what a box 6% smaller sees when the camera shakes | as above | www.trek.today | [CC BY 2.0](https://creativecommons.org/licenses/by/2.0/) |

The crops are changes of the original photos: cut down, and for vaillant-22-5-b.png also enlarged. None of the authors endorses watchglass.
