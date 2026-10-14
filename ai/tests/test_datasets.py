"""How the evaluation reads its public datasets (standard library only)."""

import os
import tempfile
import unittest

from eval import datasets


class OpenImagesLabelsTest(unittest.TestCase):
    def test_reads_both_ways_of_writing_a_verified_label(self):
        # Crowdsourced labels are written 1/0, the 540,000 others 1.0/0.0;
        # reading only "1" as present once turned most cats into "no cat"
        # (docs/investigations/2026-10-13-open-images-positives-read-as-absent.md).
        with tempfile.TemporaryDirectory() as root:
            os.makedirs(os.path.join(root, datasets.OPENIMAGES))
            path = os.path.join(root, datasets.OPENIMAGES, "oidv7-val-annotations-human-imagelabels.csv")
            with open(path, "w", encoding="utf-8", newline="\n") as file:
                file.write("ImageID,Source,LabelName,Confidence\n"
                           "a,verification,/m/01yrx,1.0\n"
                           "a,verification,/m/0bt9lr,0.0\n"
                           "b,crowdsource-verification,/m/01yrx,1\n"
                           "b,crowdsource-verification,/m/0bt9lr,0\n")
            verified = datasets.openimages_verified(root)
        self.assertEqual(verified["a"], {"/m/01yrx": True, "/m/0bt9lr": False})
        self.assertEqual(verified["b"], {"/m/01yrx": True, "/m/0bt9lr": False})


if __name__ == "__main__":
    unittest.main()
