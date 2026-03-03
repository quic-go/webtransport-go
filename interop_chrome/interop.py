#!/usr/bin/env python3

import sys

from selenium import webdriver
from selenium.webdriver.chrome.service import Service
from webdriver_manager.chrome import ChromeDriverManager
from selenium.webdriver.common.desired_capabilities import DesiredCapabilities
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions
from selenium.webdriver.common.by import By
from selenium.common.exceptions import TimeoutException

chrome_loc = "/usr/bin/google-chrome"
if sys.platform == "darwin":
    chrome_loc = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

options = webdriver.ChromeOptions()
options.gpu = False
options.binary_location = chrome_loc
options.add_argument("--no-sandbox")
options.add_argument("--enable-quic")
options.add_argument("--headless")
options.add_argument("--origin-to-force-quic-on=localhost:12345")
options.add_argument("--host-resolver-rules='MAP localhost:12345 127.0.0.1:12345'")
options.set_capability("goog:loggingPrefs", {"browser": "ALL"})

driver = webdriver.Chrome(
    service=Service(ChromeDriverManager().install()),
    options=options,
)

data = driver.execute_script('return "foobar"')

# Save data to disk
with open('output.txt', 'w') as file:
    file.write(str(data))

# delay = 5
# failed = False
# try:
#     # when the test finishes successfully, it adds a div#done to the body
#     myElem = WebDriverWait(driver, delay).until(
#         expected_conditions.presence_of_element_located((By.ID, "done"))
#     )
#     print("Test succeeded!")
# except TimeoutException:
#     failed = True
#     print("Test timed out.")
#
# # for debugging, print all the console messages
# for entry in driver.get_log("browser"):
#     print(entry)
#
# driver.quit()
#
# if failed:
#     sys.exit(1)
