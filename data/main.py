import numpy as np
import pandas as pd


def main():
    num_rows = 9_999_998

    data = {
        "Trade_ID": np.arange(1, num_rows + 1),
        "Symbol": np.random.choice(["BTC", "ETH", "SOL", "ADA", "XRP"], size=num_rows),
        "Price": np.random.uniform(10.0, 60000.0, size=num_rows),
        "Quantity": np.random.randint(1, 100, size=num_rows),
        "Is_Valid": np.random.choice([0, 1], size=num_rows, p=[0.05, 0.95]),
    }

    df = pd.DataFrame(data)
    df.to_csv("trades.csv", index=False)

    with open("trades.csv", "a") as f:
        # Short row: only 3 fields instead of 5
        f.write(f"{num_rows + 1},BTC,100.00\n")
        # Long line: symbol padded to 1010 chars, total line > 1023 bytes
        f.write(f"{num_rows + 2},{'A' * 1010},100.00,1,1\n")


if __name__ == "__main__":
    main()
