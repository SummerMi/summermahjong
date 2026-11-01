from fastapi import FastAPI
from fastapi import HTTPException

app = FastAPI()

@app.get("/")
def hello():
    return {"message":"Hello,FastAPI!"}


@app.get("/hello/{name}")
def greet(name:str):
    return {"message":f"你好，{name}!"}

@app.get("/add")
def add(a:int,b:int):
    return a+b

# @app.get("/books")
# def search_books(keywords:str , page : int=1):
#     return {"keyword":keywords,"page":page}

@app.get("/subtract")
def subtract(a:int,b:int):
    return {"result":a-b}

@app.get("/divide")
def divide(a:float,b:float):
    return {"result":a/b}





    
# A Little Secret

# I wonder if the stars can see,  
# Little things you mean to me.  
# I find you in the morning light,  
# Keeping thoughts of you at night.  

# Every breeze feels soft and new,  
# Yet somehow brings me back to you.  
# On days when skies are turning blue,  
# Under those skies, I think of you.


