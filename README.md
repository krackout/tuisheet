Presenting **tuisheet**: A love letter to [VisiCalc](http://danbricklin.com/visicalc.htm) and [Lotus 1-2-3](https://en.wikipedia.org/wiki/Lotus_1-2-3).

The motive was to code a programme inspired by VisiCalc and Lotus 1-2-3 interface, running on terminal, but fully compatible with the current universal standard of OpenXML (xlsx files for spreadsheets). Coded in Go lang; apart from my being more familiar with this programming language, it helps making a single executable, low on resources. Also, I like Go's compiler ability to cross compile to many platforms and OSes (which I personally often meet through my daily job). By the way, **tuisheet** is much more thoroughly tested and used on Linux than on Windows (or Solaris, AIX, BSD).

Mostly vibe coded. Yet with no subscription available, I used a variety of platforms and LLMs to reach a respectful stage to publish it. Started with questions on LLM web sites, then saw an advertisment for Codex with ChatGPT which I used a bit; but free tokens ran out quickly. Moved to [OpenCode agent](https://opencode.ai/) with which I went all along, changing LLMs whenever they stopped being available on the platform (Deepseek free, 0x Alpha, Muse Spark Free). A happy journey of acquittance with LLMs and agents. I can't tell of the effect of LLM switching on the code; I tried to cleanup and optimize, either by my actions or by prompts. Nevertheless the programme is stable and fast for my daily usage, having completely replaced LibreOffice or Excel.

Quick info:

- **F1** key shows help.

- **/ (slash)**  presents menu. You can navigate either by pressing the capital letter of each option (usually the first letter of its name but not always) or by arrow keys.  
For example, to open a file, the key sequence is: / F R  
To quit, / Q

- Mouse can be used to click to cells. Right click is available in cells, rows, columns and sheets/tabs bar.

**Supported formulas:**  

| Function | Description |
|---|---|
| ABS | Absolute value |
| AND | TRUE if all arguments are true |
| AVERAGE | Arithmetic mean of values |
| AVERAGEIF | Mean of cells matching a criteria |
| COLUMN | Column number of a reference |
| COUNT | Count of numeric values |
| COUNTA | Count of non-empty values |
| COUNTIF | Count of cells matching a criteria |
| COUNTIFS | Count matching multiple criteria pairs |
| DATE | Date serial from year, month, day |
| DAY | Day of month of a date serial |
| DAYS | Days between two dates |
| EXACT | Case-sensitive text comparison |
| EXP | e raised to a power |
| FIND | Position of text within text |
| HLOOKUP | Horizontal lookup in first table row |
| IF | Value depending on a condition |
| IFS | First matching condition's value |
| INDEX | Value at row/column inside a range |
| INT | Round down to integer |
| IPMT | Interest portion of a loan payment |
| ISBLANK | TRUE if the cell is empty |
| ISERROR | TRUE if the expression fails |
| ISNUMBER | TRUE if the value is a number |
| ISTEXT | TRUE if the value is text |
| LEFT | First characters of text |
| LEN | Length of text in characters |
| LN | Natural logarithm |
| LOWER | Text converted to lowercase |
| MATCH | Position of a value in a range |
| MAX | Largest numeric value |
| MID | Substring by start and length |
| MIN | Smallest numeric value |
| MOD | Remainder with sign of divisor |
| MONTH | Month of a date serial |
| NOT | Logical negation |
| NOW | Current date and time serial |
| OR | TRUE if any argument is true |
| PI | The number pi |
| PMT | Loan payment for fixed rate/terms |
| POWER | Number raised to a power |
| PPMT | Principal portion of a loan payment |
| PRODUCT | Product of values |
| RIGHT | Last characters of text |
| ROUND | Round to given decimals |
| ROUNDUP | Round away from zero |
| ROW | Row number of a reference |
| ROWS | Number of rows in a reference |
| SQRT | Square root |
| SUBSTITUTE | Replace occurrences of text |
| SUBTOTAL | Aggregate ignoring nested SUBTOTALs |
| SUM | Sum of values |
| SUMIF | Sum of cells matching a criteria |
| SUMIFS | Sum matching multiple criteria pairs |
| TODAY | Current date serial |
| TRIM | Strip and collapse spaces |
| TRUE/FALSE | Boolean literals |
| UPPER | Text converted to uppercase |
| VLOOKUP | Vertical lookup in first table column |
| WEEKDAY | Day of week of a date serial |
| XLOOKUP | Modern lookup with match modes |
| YEAR | Year of a date serial |
| IFERROR | Fallback when an expression errors |
| IFNA | Fallback on #N/A like IFERROR |
| NA | The #N/A error value |
| INDIRECT | Reference from text (A1 or R1C1, with sheet) |
| LET | Bind names to values for a calculation |
| OFFSET | Range offset by rows/cols with optional height/width |
| TEXT | Format a value as text using a number format |
| VALUE | Convert text that looks like a number to a number |
| FILTER | Filter array rows/cols by include booleans with optional if_empty |
| TEXTJOIN | Join text with delimiter, optionally ignoring empty |
| SUMPRODUCT | Sum of products of corresponding array elements |


Screenshots:  

**Formulas:**  
<img width="930" height="619" alt="scrot-2026-09-17_13-03-29_930x619" src="https://github.com/user-attachments/assets/7a5c08ea-f891-44ac-be0d-32493981be11" />

**Undo history:**  
<img width="927" height="618" alt="scrot-2026-09-17_13-04-34_927x618" src="https://github.com/user-attachments/assets/8155c9d8-d262-4396-afde-e6e3179991c6" />

**Search results:**  
<img width="928" height="620" alt="scrot-2026-09-17_13-06-21_928x620" src="https://github.com/user-attachments/assets/05fbee79-5d96-41d4-a209-882de9304dfd" />

**File selector to load file:**  
<img width="839" height="576" alt="scrot-2026-09-17_13-07-35_839x576" src="https://github.com/user-attachments/assets/ea870424-e9d3-4cf6-b2bb-69a5a05be489" />

**Perspective view:**  
<img width="839" height="575" alt="scrot-2026-09-17_13-10-54_839x575" src="https://github.com/user-attachments/assets/424cb624-6a84-4c09-a118-cfe41b238fb9" />

---
Enjoy! Regarding your precious xlsx files, I quote from licence,  

> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
